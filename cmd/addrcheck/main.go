package main

import (
    "encoding/hex"
    "encoding/json"
    "fmt"
    "os"
    
    anchorTaproot "github.com/0ceanslim/anchor/pkg/taproot"
    "github.com/vulpemventures/go-elements/network"
)

func main() {
    data, _ := os.ReadFile("pool.json")
    var pool map[string]struct {
        CMR     string `json:"cmr"`
        Address string `json:"address"`
    }
    json.Unmarshal(data, &pool)
    cmrBytes, _ := hex.DecodeString(pool["pool_creation"].CMR)
    net := network.Testnet
    addr, _ := anchorTaproot.Address(cmrBytes, &net)
    fmt.Println("computed:", addr)
    fmt.Println("pool.json:", pool["pool_creation"].Address)
    fmt.Println("match:", addr == pool["pool_creation"].Address)
    cb, _ := anchorTaproot.ControlBlock(cmrBytes)
    fmt.Println("cb_len:", len(cb), "cb_hex:", hex.EncodeToString(cb))
}
