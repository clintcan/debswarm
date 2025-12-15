package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/debswarm/debswarm/internal/p2p"
	"github.com/spf13/cobra"
)

func pskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "psk",
		Short: "Manage Pre-Shared Keys for private swarms",
		Long: `Manage Pre-Shared Keys (PSK) for creating private debswarm networks.

PSK allows you to create isolated swarms that only allow connections from
nodes that have the same key. This is useful for corporate networks or
other private deployments.`,
	}

	cmd.AddCommand(pskGenerateCmd())
	cmd.AddCommand(pskShowCmd())

	return cmd
}

func pskGenerateCmd() *cobra.Command {
	var outputPath string

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate a new PSK file",
		Long: `Generate a new random Pre-Shared Key and save it to a file.

The generated file uses the standard libp2p PSK format and can be
distributed to all nodes that should participate in the private swarm.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			psk, err := p2p.GeneratePSK()
			if err != nil {
				return fmt.Errorf("failed to generate PSK: %w", err)
			}

			// Determine output path
			if outputPath == "" {
				homeDir, _ := os.UserHomeDir()
				outputPath = filepath.Join(homeDir, ".config", "debswarm", "swarm.key")
			}

			// Create parent directory with restricted permissions
			if err := os.MkdirAll(filepath.Dir(outputPath), 0750); err != nil {
				return fmt.Errorf("failed to create directory: %w", err)
			}

			// Save PSK
			if err := p2p.SavePSK(psk, outputPath); err != nil {
				return fmt.Errorf("failed to save PSK: %w", err)
			}

			fmt.Printf("Generated new PSK\n")
			fmt.Printf("══════════════════════════════════════\n")
			fmt.Printf("File:        %s\n", outputPath)
			fmt.Printf("Fingerprint: %s\n", p2p.PSKFingerprint(psk))
			fmt.Printf("\nTo use this PSK, add to config.toml:\n")
			fmt.Printf("  [privacy]\n")
			fmt.Printf("  psk_path = %q\n", outputPath)
			fmt.Printf("\nOr distribute swarm.key to all nodes in your private swarm.\n")

			return nil
		},
	}
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output file path (default: ~/.config/debswarm/swarm.key)")

	return cmd
}

func pskShowCmd() *cobra.Command {
	var pskPath string

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show PSK fingerprint",
		Long: `Display the fingerprint of a PSK file without revealing the actual key.

The fingerprint can be shared to verify that nodes are using the same PSK.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load from config if no path specified
			if pskPath == "" {
				cfg, err := loadConfig()
				if err != nil {
					return err
				}
				if cfg.Privacy.PSKPath != "" {
					pskPath = cfg.Privacy.PSKPath
				} else if cfg.Privacy.PSK != "" {
					psk, err := p2p.ParsePSKFromHex(cfg.Privacy.PSK)
					if err != nil {
						return fmt.Errorf("invalid inline PSK: %w", err)
					}
					fmt.Printf("PSK Fingerprint (from config)\n")
					fmt.Printf("══════════════════════════════════════\n")
					fmt.Printf("Fingerprint: %s\n", p2p.PSKFingerprint(psk))
					fmt.Printf("Source:      inline config\n")
					return nil
				} else {
					return fmt.Errorf("no PSK configured; use --file or configure psk_path in config.toml")
				}
			}

			psk, err := p2p.LoadPSK(pskPath)
			if err != nil {
				return fmt.Errorf("failed to load PSK: %w", err)
			}

			fmt.Printf("PSK Fingerprint\n")
			fmt.Printf("══════════════════════════════════════\n")
			fmt.Printf("File:        %s\n", pskPath)
			fmt.Printf("Fingerprint: %s\n", p2p.PSKFingerprint(psk))

			return nil
		},
	}
	cmd.Flags().StringVarP(&pskPath, "file", "f", "", "PSK file path (default: from config)")

	return cmd
}
