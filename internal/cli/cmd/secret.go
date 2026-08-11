// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"

	"github.com/podmin-dev/podmin/internal/secrets"
	"github.com/spf13/cobra"
)

// secretScope contains flags inherited by every secret operation.
type secretScope struct {
	provider  string
	pod       string
	namespace string
}

// secretCommand creates the secret management command group.
func secretCommand() *cobra.Command {
	scope := &secretScope{}
	root := &cobra.Command{Use: "secret"}
	root.PersistentFlags().StringVar(&scope.provider, "provider", "", "secret provider (defaults to current context)")
	root.PersistentFlags().StringVar(&scope.pod, "for", "", "Pod name")
	root.PersistentFlags().StringVarP(&scope.namespace, "namespace", "n", "default", "Pod namespace")
	_ = root.MarkPersistentFlagRequired("for")
	root.AddCommand(secretCreateCommand(scope), secretUpdateCommand(scope), secretListCommand(scope), secretDeleteCommand(scope), secretRestoreCommand(scope), secretDestroyCommand(scope))
	return root
}

// secretTarget constructs the selected manager and validated scope prefix.
func secretTarget(cmd *cobra.Command, scope *secretScope) (secrets.Manager, string, error) {
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
	prefix, err := secrets.Prefix(selected.ClusterID, scope.namespace, scope.pod)
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
	return store, prefix, nil
}
