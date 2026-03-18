package main

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "anchor",
	Short: "Anchor: Liquid AMM tool for Simplicity contracts",
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(cmdCompile())
	rootCmd.AddCommand(cmdCreatePool())
	rootCmd.AddCommand(cmdPoolInfo())
	rootCmd.AddCommand(cmdSwap())
	rootCmd.AddCommand(cmdAddLiquidity())
	rootCmd.AddCommand(cmdRemoveLiquidity())
	rootCmd.AddCommand(cmdCheck())
	rootCmd.AddCommand(cmdFindPools())
}
