package main

import (
	"fmt"

	"github.com/0ceanslim/anchor/pkg/pool"
	"github.com/0ceanslim/anchor/pkg/rpc"
	"github.com/spf13/cobra"
)

func cmdPoolInfo() *cobra.Command {
	var poolFile, rpcURL, rpcUser, rpcPass string
	cmd := &cobra.Command{
		Use:   "pool-info",
		Short: "Query live pool reserves from chain",
		RunE: func(cmd *cobra.Command, args []string) error {
			rpcURL, rpcUser, rpcPass = resolveRPC(rpcURL, rpcUser, rpcPass)
			resolved, err := resolvePoolFile(cmd, poolFile)
			if err != nil {
				return err
			}
			if resolved == "" {
				return fmt.Errorf("no pool config found — use 'anchor find-pools --save' to discover one, or specify --pool")
			}
			cfg, err := pool.Load(resolved)
			if err != nil {
				return err
			}
			client := rpc.New(rpcURL, rpcUser, rpcPass)
			state, err := pool.Query(cfg, client)
			if err != nil {
				return err
			}
			fmt.Printf("Reserve0 (Asset0): %d sat\n", state.Reserve0)
			fmt.Printf("Reserve1 (Asset1): %d sat\n", state.Reserve1)
			fmt.Printf("Total Supply (LP):  %d sat\n", state.TotalSupply())
			fmt.Printf("LP Reserve:         %d sat\n", state.LPReserve)
			fmt.Printf("Pool A UTXO:        %s:%d\n", state.PoolATxID, state.PoolAVout)
			fmt.Printf("Pool B UTXO:        %s:%d\n", state.PoolBTxID, state.PoolBVout)
			fmt.Printf("LP Reserve UTXO:    %s:%d\n", state.LpReserveTxID, state.LpReserveVout)
			return nil
		},
	}
	cmd.Flags().StringVar(&poolFile, "pool", "pool.json", "Pool config file")
	cmd.Flags().StringVar(&rpcURL, "rpc-url", "", "Elements RPC URL (env: ANCHOR_RPC_URL)")
	cmd.Flags().StringVar(&rpcUser, "rpc-user", "", "RPC username (env: ANCHOR_RPC_USER)")
	cmd.Flags().StringVar(&rpcPass, "rpc-pass", "", "RPC password (env: ANCHOR_RPC_PASS)")
	return cmd
}
