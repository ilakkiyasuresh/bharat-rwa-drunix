// Command dr is the depositary receipt chaincode for the Bharat RWA network.
//
// It targets Drunix, NPCI's enhanced fork of Hyperledger Fabric v2.5.x, and
// deploys as an ordinary Fabric v2 external or packaged chaincode because
// Drunix maintains backward compatibility with the Fabric chaincode lifecycle.
//
// Build:  go build -o dr ./...
// Package and deploy: see network/README.md
package main

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

func main() {
	cc, err := contractapi.NewChaincode(&DRContract{})
	if err != nil {
		log.Panicf("creating dr chaincode: %v", err)
	}
	if err := cc.Start(); err != nil {
		log.Panicf("starting dr chaincode: %v", err)
	}
}

// unmarshal decodes a JSON argument, naming the target type in the error so a
// malformed argument is diagnosable from the peer log alone.
func unmarshal(s string, v any) error {
	if s == "" {
		return fmt.Errorf("empty payload for %T", v)
	}
	if err := json.Unmarshal([]byte(s), v); err != nil {
		return fmt.Errorf("decoding %T: %w", v, err)
	}
	return nil
}
