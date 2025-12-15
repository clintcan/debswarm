package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/debswarm/debswarm/internal/p2p"
	"github.com/spf13/cobra"
)

func identityCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "identity",
		Short: "Manage node identity",
		Long: `Manage the persistent identity key for this node.

The identity key determines your peer ID in the P2P network. By default,
debswarm generates a new ephemeral identity on each start. When a data
directory is configured, the identity is persisted for stable peer IDs.`,
	}

	cmd.AddCommand(identityShowCmd())
	cmd.AddCommand(identityRegenerateCmd())

	return cmd
}

func identityShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show current identity information",
		Long:  `Display the current peer ID and identity key file location.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			// Determine data directory
			identityDir := filepath.Join(filepath.Dir(cfg.Cache.Path), "debswarm-data")
			if dataDir != "" {
				identityDir = dataDir
			}

			keyPath := filepath.Join(identityDir, p2p.IdentityKeyFile)

			fmt.Printf("Node Identity\n")
			fmt.Printf("══════════════════════════════════════\n")

			// Check if identity file exists
			if _, err := os.Stat(keyPath); os.IsNotExist(err) {
				fmt.Printf("Status:      No persistent identity\n")
				fmt.Printf("Key File:    %s (not created yet)\n", keyPath)
				fmt.Printf("\nA persistent identity will be created when the daemon starts.\n")
				fmt.Printf("Until then, ephemeral identities are used.\n")
				return nil
			}

			// Load the identity
			privKey, err := p2p.LoadIdentity(keyPath)
			if err != nil {
				return fmt.Errorf("failed to load identity: %w", err)
			}

			peerID := p2p.IdentityFingerprint(privKey)

			fmt.Printf("Peer ID:     %s\n", peerID)
			fmt.Printf("Key File:    %s\n", keyPath)
			fmt.Printf("Key Type:    Ed25519\n")
			fmt.Printf("\nThis peer ID is stable across daemon restarts.\n")
			fmt.Printf("Share it with others to add to their peer allowlists.\n")

			return nil
		},
	}
}

func identityRegenerateCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "regenerate",
		Short: "Regenerate the identity key (WARNING: changes peer ID)",
		Long: `Generate a new identity key, replacing the existing one.

WARNING: This will change your peer ID. Other nodes will see you as a
different peer, and any peer allowlists that include your old ID will
need to be updated.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			// Determine data directory
			identityDir := filepath.Join(filepath.Dir(cfg.Cache.Path), "debswarm-data")
			if dataDir != "" {
				identityDir = dataDir
			}

			keyPath := filepath.Join(identityDir, p2p.IdentityKeyFile)

			// Check if file exists and confirm
			if _, err := os.Stat(keyPath); err == nil && !force {
				// Load current identity to show what we're replacing
				privKey, err := p2p.LoadIdentity(keyPath)
				if err == nil {
					fmt.Printf("Current Peer ID: %s\n\n", p2p.IdentityFingerprint(privKey))
				}
				return fmt.Errorf("identity file exists at %s\n\nUse --force to regenerate (this will change your peer ID)", keyPath)
			}

			// Ensure directory exists
			if err := os.MkdirAll(identityDir, 0700); err != nil {
				return fmt.Errorf("failed to create identity directory: %w", err)
			}

			// Generate new identity
			privKey, err := p2p.GenerateIdentity()
			if err != nil {
				return fmt.Errorf("failed to generate identity: %w", err)
			}

			// Save identity
			if err := p2p.SaveIdentity(privKey, keyPath); err != nil {
				return fmt.Errorf("failed to save identity: %w", err)
			}

			peerID := p2p.IdentityFingerprint(privKey)

			fmt.Printf("New Identity Generated\n")
			fmt.Printf("══════════════════════════════════════\n")
			fmt.Printf("Peer ID:     %s\n", peerID)
			fmt.Printf("Key File:    %s\n", keyPath)
			fmt.Printf("\nThis is now your stable peer ID.\n")

			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Force regeneration even if identity exists")

	return cmd
}
