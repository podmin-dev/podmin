// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"

	"github.com/podmin-dev/podmin/internal/cli/config"
	"github.com/podmin-dev/podmin/internal/manifest"
	"github.com/podmin-dev/podmin/internal/secrets"
	"github.com/spf13/cobra"
)

// connectCommand creates the connect command.
func connectCommand() *cobra.Command {
	var provider, region, profile, bucket, secretsProvider string
	c := &cobra.Command{Use: "connect <cluster-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !manifest.ValidID(args[0]) {
			return errors.New("invalid cluster ID")
		}
		if provider != "aws" {
			return errors.New("provider must be aws")
		}
		if _, err := secrets.ParseProvider(secretsProvider); err != nil {
			return err
		}
		if region == "" || bucket == "" {
			return errors.New("--region and --bucket are required")
		}
		selected := config.Context{ClusterID: args[0], Provider: provider, Region: region, Profile: profile, Bucket: bucket, SecretsProvider: secretsProvider}
		a, err := loadCloud(cmd.Context(), selected)
		if err != nil {
			return err
		}
		if err = a.Bucket.EnsureBucket(cmd.Context()); err != nil {
			return err
		}
		s, err := config.DefaultStore()
		if err != nil {
			return err
		}
		state, err := s.Load()
		if err != nil {
			return err
		}
		state.Contexts[args[0]] = selected
		state.Current = args[0]
		return s.Save(state)
	}}
	c.Flags().StringVar(&provider, "provider", "aws", "cloud provider")
	c.Flags().StringVar(&secretsProvider, "secrets-provider", string(secrets.AWSParameterStore), "default secrets provider")
	c.Flags().StringVar(&region, "region", "", "AWS region")
	c.Flags().StringVar(&profile, "profile", "", "AWS profile")
	c.Flags().StringVar(&bucket, "bucket", "", "cluster bucket")
	return c
}
