package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/0ceanslim/anchor/pkg/compiler"
	"github.com/0ceanslim/anchor/pkg/pool"
	"github.com/spf13/cobra"
)

func cmdCompile() *cobra.Command {
	var buildDir, outFile, netName string
	var asset0Hex, asset1Hex string
	var feeNum, feeDen uint64
	cmd := &cobra.Command{
		Use:   "compile",
		Short: "Compile .shl contracts and write pool.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			netName = resolveNetwork(netName)
			net, err := parseNetwork(netName)
			if err != nil {
				return err
			}
			// Patch .args files before compiling if asset/fee flags are set.
			params := map[string]compiler.ArgsParam{}
			if asset0Hex != "" {
				params["ASSET0"] = compiler.ArgsParam{Value: normalizeHex(asset0Hex), Type: "u256"}
			}
			if asset1Hex != "" {
				params["ASSET1"] = compiler.ArgsParam{Value: normalizeHex(asset1Hex), Type: "u256"}
			}
			if cmd.Flags().Changed("fee-num") || cmd.Flags().Changed("fee-den") {
				feeDiff := feeDen - feeNum
				params["FEE_NUM"] = compiler.ArgsParam{Value: fmt.Sprintf("%d", feeNum), Type: "u64"}
				params["FEE_DEN"] = compiler.ArgsParam{Value: fmt.Sprintf("%d", feeDen), Type: "u64"}
				params["FEE_DIFF"] = compiler.ArgsParam{Value: fmt.Sprintf("%d", feeDiff), Type: "u64"}
			}
			if len(params) > 0 {
				if err := compiler.PatchParams(buildDir, params); err != nil {
					return fmt.Errorf("patch params: %w", err)
				}
			}
			fmt.Fprintf(os.Stderr, "Compiling contracts from %s...\n", buildDir)
			cfg, err := compiler.CompileAll(buildDir, net)
			if err != nil {
				return err
			}
			cfg.FeeNum = feeNum
			cfg.FeeDen = feeDen
			fmt.Printf("pool_creation: %s\n", cfg.PoolCreation.Address)
			fmt.Printf("pool_a:        %s\n", cfg.PoolA.Address)
			fmt.Printf("pool_b:        %s\n", cfg.PoolB.Address)
			fmt.Printf("lp_reserve:    %s\n", cfg.LpReserve.Address)
			// Guard: if outFile already holds a deployed pool, prompt before overwriting.
			savePath := outFile
			if !cmd.Flags().Changed("out") {
				if existing, loadErr := pool.Load(outFile); loadErr == nil && existing.LPAssetID != "" {
					fmt.Fprintf(os.Stderr, "\nWARNING: %s already contains a deployed pool:\n", outFile)
					fmt.Fprintf(os.Stderr, "  asset0:      %s\n", existing.Asset0)
					fmt.Fprintf(os.Stderr, "  asset1:      %s\n", existing.Asset1)
					fmt.Fprintf(os.Stderr, "  fee:         %d/%d\n", existing.FeeNum, existing.FeeDen)
					fmt.Fprintf(os.Stderr, "  lp_asset_id: %s\n", existing.LPAssetID)
					autoName := poolJSONName(existing.Asset0, existing.Asset1, existing.FeeNum, existing.FeeDen)
					fmt.Fprintf(os.Stderr, "\n  [o] Overwrite %s\n", outFile)
					fmt.Fprintf(os.Stderr, "  [n] Save as new file: %s\n", autoName)
					fmt.Fprintf(os.Stderr, "  Or type a custom filename: ")
					choice := strings.TrimSpace(promptString(""))
					switch strings.ToLower(choice) {
					case "o":
						// overwrite — savePath stays as outFile
					case "n":
						savePath = autoName
					case "":
						fmt.Fprintf(os.Stderr, "Aborted.\n")
						return nil
					default:
						savePath = choice
					}
				}
			}
			if err := cfg.Save(savePath); err != nil {
				return fmt.Errorf("write %s: %w", savePath, err)
			}
			fmt.Fprintf(os.Stderr, "Wrote %s\n", savePath)
			return nil
		},
	}
	cmd.Flags().StringVar(&buildDir, "build-dir", "./build", "Directory containing .shl files")
	cmd.Flags().StringVar(&outFile, "out", "pool.json", "Output pool.json path")
	cmd.Flags().StringVar(&netName, "network", "", "Network: liquid, testnet, regtest (env: ANCHOR_NETWORK)")
	cmd.Flags().StringVar(&asset0Hex, "asset0", "", "Asset0 ID (64-char hex) — patches .args before compiling")
	cmd.Flags().StringVar(&asset1Hex, "asset1", "", "Asset1 ID (64-char hex) — patches .args before compiling")
	cmd.Flags().Uint64Var(&feeNum, "fee-num", 997, "Fee numerator — patches .args before compiling")
	cmd.Flags().Uint64Var(&feeDen, "fee-den", 1000, "Fee denominator — patches .args before compiling")
	return cmd
}
