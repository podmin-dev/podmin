// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/podmin-dev/podmin/internal/cli/deploy"
	"github.com/podmin-dev/podmin/internal/manifest"
	"github.com/podmin-dev/podmin/internal/secrets"
	"github.com/spf13/cobra"
)

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// parseEnv validates built-in manifest environment settings without overwriting duplicates.
func parseEnv(values []string) (map[string]string, error) {
	result := map[string]string{}
	for _, raw := range values {
		key, value, explicit := strings.Cut(raw, "=")
		key = strings.TrimSpace(key)
		if !envNamePattern.MatchString(key) {
			return nil, fmt.Errorf("invalid environment variable name %q", key)
		}
		if _, duplicate := result[key]; duplicate {
			return nil, fmt.Errorf("duplicate environment variable %q", key)
		}
		if !explicit {
			var found bool
			value, found = os.LookupEnv(key)
			if !found {
				return nil, fmt.Errorf("environment variable %s is not set", key)
			}
		}
		result[key] = value
	}
	return result, nil
}

// parseSecretKeys validates and sorts built-in manifest secret keys.
func parseSecretKeys(values []string) ([]string, error) {
	seen := map[string]bool{}
	for _, key := range values {
		if !manifest.ValidID(key) {
			return nil, fmt.Errorf("invalid secret key %q", key)
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate secret key %q", key)
		}
		seen[key] = true
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result, nil
}

// parsePorts validates repeatable comma-separated SERVICE:TARGET TCP port mappings.
func parsePorts(values []string) ([]manifest.ServicePort, error) {
	var result []manifest.ServicePort
	seen := map[int]bool{}
	for _, value := range values {
		for _, raw := range strings.Split(value, ",") {
			service, target, found := strings.Cut(strings.TrimSpace(raw), ":")
			service, target = strings.TrimSpace(service), strings.TrimSpace(target)
			servicePort, serviceErr := strconv.Atoi(service)
			targetPort, targetErr := strconv.Atoi(target)
			if !found || serviceErr != nil || targetErr != nil || servicePort < 1 || servicePort > 65535 || targetPort < 1 || targetPort > 65535 {
				return nil, fmt.Errorf("invalid port mapping %q; expected SERVICE:TARGET integers from 1 through 65535", raw)
			}
			if seen[servicePort] {
				return nil, fmt.Errorf("duplicate Service port TCP/%d", servicePort)
			}
			seen[servicePort] = true
			result = append(result, manifest.ServicePort{Protocol: "TCP", Port: servicePort, TargetPort: targetPort})
		}
	}
	return result, nil
}

// deployCommand creates the deployment command.
func deployCommand() *cobra.Command {
	var file, nodeGroup string
	var images, envValues, secretKeys, portValues []string
	var service bool
	c := &cobra.Command{Use: "deploy <name>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !manifest.ValidID(nodeGroup) {
			cmd.Root().SilenceUsage = false
			return errors.New("invalid nodegroup ID")
		}
		var b []byte
		var err error
		overrides := images
		if cmd.Flags().Changed("file") {
			if cmd.Flags().Changed("env") || cmd.Flags().Changed("secret") || cmd.Flags().Changed("port") {
				return errors.New("--env, --secret, and --port cannot be used with --file")
			}
			b, err = os.ReadFile(file)
		} else {
			if cmd.Flags().Changed("port") && !service {
				return errors.New("--port requires --service")
			}
			env, parseErr := parseEnv(envValues)
			if parseErr != nil {
				return parseErr
			}
			keys, keyErr := parseSecretKeys(secretKeys)
			if keyErr != nil {
				return keyErr
			}
			ports, portErr := parsePorts(portValues)
			if portErr != nil {
				return portErr
			}
			provider := ""
			if len(keys) > 0 {
				selected, contextErr := currentContext()
				if contextErr != nil {
					return contextErr
				}
				if _, contextErr = secrets.ParseProvider(selected.SecretsProvider); contextErr != nil {
					return contextErr
				}
				provider = selected.SecretsProvider
			}
			b, err = manifest.Init(manifest.InitConfig{
				Name:            args[0],
				NodeGroup:       nodeGroup,
				Namespace:       "default",
				Images:          images,
				Service:         service,
				Env:             env,
				Ports:           ports,
				SecretKeys:      keys,
				SecretsProvider: provider,
			})
			overrides = nil
		}
		if err != nil {
			return err
		}
		parsed, err := manifest.ParseDeployment(b, overrides, "", args[0], nodeGroup)
		if err != nil {
			return err
		}
		if service && parsed.Service == nil {
			return errors.New("--service requires the manifest to contain a Service")
		}
		name, err := manifest.Name(parsed.Pod)
		if err != nil {
			return err
		}
		if name != args[0] {
			return fmt.Errorf("metadata.name %q does not equal deployment %q", name, args[0])
		}
		if _, err = fmt.Fprintf(cmd.OutOrStdout(), "Deploying %s to NodeGroup %s...\n", args[0], nodeGroup); err != nil {
			return err
		}
		a, err := currentCloud(cmd)
		if err != nil {
			return err
		}
		if err = deploy.Apply(cmd.Context(), a.Objects, nodeGroup, args[0], parsed); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Committed %s to NodeGroup %s; nodes will reconcile it asynchronously.\n", args[0], nodeGroup)
		return err
	}}
	c.Flags().StringVarP(&file, "file", "f", "", "manifest file")
	c.Flags().StringVarP(&nodeGroup, "nodegroup", "g", "", "nodegroup ID")
	c.Flags().StringArrayVar(&images, "image", nil, "image override")
	c.Flags().StringArrayVarP(&envValues, "env", "e", nil, "environment variable (KEY=VALUE or KEY to inherit)")
	c.Flags().StringArrayVar(&portValues, "port", nil, "Service port mapping (SERVICE:TARGET, comma-separated or repeatable)")
	c.Flags().StringArrayVar(&secretKeys, "secret", nil, "secret key to mount from the context default provider")
	c.Flags().BoolVar(&service, "service", false, "include or require a Service (built-in port 443)")
	_ = c.MarkFlagRequired("nodegroup")
	return c
}
