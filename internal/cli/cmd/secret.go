// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"
	"fmt"

	"github.com/podmin-dev/podmin/internal/manifest"
	"github.com/podmin-dev/podmin/internal/secrets"
	"github.com/spf13/cobra"
)

// secretScope contains flags inherited by every secret operation.
type secretScope struct {
	provider  string
	pod       string
	namespace string
	system    bool
}

// secretCommand creates the secret management command group.
func secretCommand() *cobra.Command {
	scope := &secretScope{}
	root := &cobra.Command{Use: "secret"}
	root.PersistentFlags().StringVar(&scope.provider, "provider", "", "secret provider (defaults to current context)")
	root.PersistentFlags().StringVar(&scope.pod, "for", "", "Pod name")
	root.PersistentFlags().StringVarP(&scope.namespace, "namespace", "n", "default", "Pod namespace")
	root.PersistentFlags().BoolVar(&scope.system, "system", false, "manage an allowlisted Podmin system secret")
	root.AddCommand(secretCreateCommand(scope), secretUpdateCommand(scope), secretListCommand(scope), secretDeleteCommand(scope), secretRestoreCommand(scope), secretDestroyCommand(scope))
	return root
}

// secretTarget constructs the selected manager and validated scope prefix.
func secretTarget(cmd *cobra.Command, scope *secretScope) (secrets.Manager, string, error) {
	if err := validateSecretScope(cmd, scope); err != nil {
		return nil, "", err
	}
	selected, err := currentContext()
	if err != nil {
		return nil, "", err
	}
	providerName := scope.provider
	if providerName == "" {
		providerName = selected.SecretsProvider
	}
	provider, err := secrets.ParseProvider(providerName)
	if err != nil {
		return nil, "", err
	}
	a, err := loadCloud(cmd.Context(), selected)
	if err != nil {
		return nil, "", err
	}
	store, ok := a.SecretStores[provider]
	if !ok {
		return nil, "", fmt.Errorf("secret provider %q is unavailable from cloud provider %q", provider, selected.Provider)
	}
	if scope.system {
		prefix, prefixErr := secrets.SystemPrefix(selected.ClusterID)
		return store, prefix, prefixErr
	}
	prefix, err := secrets.Prefix(selected.ClusterID, scope.namespace, scope.pod)
	if err != nil {
		return nil, "", err
	}
	return store, prefix, nil
}

// validateSecretScope enforces mutually exclusive workload and system addressing.
func validateSecretScope(cmd *cobra.Command, scope *secretScope) error {
	if scope.system {
		if scope.pod != "" || scope.provider != "" || cmd.Flags().Changed("namespace") {
			return errors.New("--system cannot be combined with --for, --namespace, or --provider")
		}
		return nil
	}
	if scope.pod == "" {
		return errors.New("--for is required unless --system is set")
	}
	return nil
}

// validateSecretKey permits ordinary workload keys or explicitly allowlisted system keys.
func validateSecretKey(scope *secretScope, key string) error {
	if scope.system {
		if !secrets.ManageableSystemKey(key) {
			return fmt.Errorf("system secret %q is not user-manageable", key)
		}
		return nil
	}
	if !manifest.ValidID(key) {
		return errors.New("invalid secret key")
	}
	return nil
}

// manageableSystemKeys removes internal system-secret names from user-visible listings.
func manageableSystemKeys(names []string) []string {
	allowed := make([]string, 0, len(names))
	for _, name := range names {
		if secrets.ManageableSystemKey(name) {
			allowed = append(allowed, name)
		}
	}
	return allowed
}
