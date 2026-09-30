# Deploying to a Drunix network

Drunix keeps the Fabric v2 chaincode lifecycle, so this deploys as an ordinary
Fabric v2.5 chaincode. Nothing here is Drunix-specific at the packaging level;
what is Drunix-specific is the peer topology it expects and the state database
it is designed against.

## Status

This chaincode builds, vets and passes its tests. The deployment below is
written against the Drunix test network and has **not** been executed for this
submission. The demo in `make demo` runs the same domain logic through the
in-memory Store instead, which exercises the rules but not endorsement,
ordering or private data dissemination.

Saying so plainly is deliberate. A repository that implies a running seven-node
network when it has one is worth less than one that is exact about what has
been done.

## Prerequisites

```bash
git clone https://github.com/npci/drunix
cd drunix && make peer orderer
export PATH=$PWD/build/bin:$PATH
```

## 1. Bring up the network

NPCI's Falcon, their earlier open-source Fabric orchestration tool for
Kubernetes, is built for this topology and is the intended path for anything
beyond a laptop.

```bash
cd drunix/test-network
./network.sh up -ca -s couchdb
```

For the Drunix SQL state database, set the peer to the YugabyteDB provider
instead. This is the single biggest technical reason to be on Drunix for this
use case: the register becomes SQL-queryable, so beneficial-ownership lookups,
concentration checks and regulatory extracts are joins rather than chaincode
range scans.

```yaml
# core.yaml
ledger:
  state:
    stateDatabase: sql
    sqlConfig:
      driver: yugabyte
      dataSourceName: "postgresql://drunix@yb-tserver:5433/ledger?sslmode=require"
```

## 2. Create the channels

```bash
configtxgen -profile RegistryChannel     -outputBlock ./channel-artifacts/registry.block      -channelID registry
configtxgen -profile IssuanceChannelCRE  -outputBlock ./channel-artifacts/issuance-cre.block  -channelID issuance-cre
configtxgen -profile OpsChannel          -outputBlock ./channel-artifacts/ops.block           -channelID ops

osnadmin channel join --channelID issuance-cre \
  --config-block ./channel-artifacts/issuance-cre.block \
  -o localhost:7053 --ca-file "$ORDERER_CA" \
  --client-cert "$ORDERER_ADMIN_TLS_SIGN_CERT" --client-key "$ORDERER_ADMIN_TLS_PRIVATE_KEY"
```

## 3. Package and install the chaincode

```bash
cd ../../chaincode/dr
GOFLAGS=-mod=mod go mod download
peer lifecycle chaincode package dr.tar.gz --path . --lang golang --label dr_1.0

# Install on every peer that endorses: both Committing Peers and the Lite
# Peers belonging to compliance, the sponsor and the valuer.
peer lifecycle chaincode install dr.tar.gz
export PACKAGE_ID=$(peer lifecycle chaincode queryinstalled --output json \
  | jq -r '.installed_chaincodes[] | select(.label=="dr_1.0") | .package_id')
```

## 4. Approve with the endorsement policy and collections

The policy is the control. See `endorsement-policies.md` for why each function
carries the signatures it does.

```bash
peer lifecycle chaincode approveformyorg \
  --channelID issuance-cre --name dr --version 1.0 --sequence 1 \
  --signature-policy "AND('CitiDepositaryMSP.peer','RegistrarMSP.peer','ComplianceMSP.peer')" \
  --collections-config ../../network/collections_config.json \
  --package-id "$PACKAGE_ID" \
  --orderer localhost:7050 --tls --cafile "$ORDERER_CA"

# Repeat for each organisation, then commit once the lifecycle policy is met.
peer lifecycle chaincode commit \
  --channelID issuance-cre --name dr --version 1.0 --sequence 1 \
  --signature-policy "AND('CitiDepositaryMSP.peer','RegistrarMSP.peer','ComplianceMSP.peer')" \
  --collections-config ../../network/collections_config.json \
  --orderer localhost:7050 --tls --cafile "$ORDERER_CA" \
  --peerAddresses localhost:7051 --tlsRootCertFiles "$DEPOSITARY_CA" \
  --peerAddresses localhost:8051 --tlsRootCertFiles "$REGISTRAR_CA" \
  --peerAddresses localhost:10051 --tlsRootCertFiles "$COMPLIANCE_CA"
```

## 5. Run the narrative against the network

```bash
# Create the scheme.
peer chaincode invoke -C issuance-cre -n dr \
  -c '{"function":"CreateScheme","Args":["{\"id\":\"CRE-BLR-001\",\"assetName\":\"Prestige Tech Park, Tower C\",\"totalUnits\":100000,\"unitPrice\":1000000,\"minTicketUnits\":1000,\"lockupDays\":90,\"holderCap\":200,\"concentrationCapBps\":2500,\"eligibleClasses\":[\"INSTITUTIONAL\",\"HNI\"],\"permittedJurisdictions\":[\"IN\"],\"preferredReturnBps\":800,\"promoteBps\":2000,\"status\":\"SUBSCRIBING\"}"]}' \
  --tls --cafile "$ORDERER_CA" \
  --peerAddresses localhost:7051 --tlsRootCertFiles "$DEPOSITARY_CA" \
  --peerAddresses localhost:8051 --tlsRootCertFiles "$REGISTRAR_CA" \
  --peerAddresses localhost:9051 --tlsRootCertFiles "$TRUSTEE_CA"

# A transfer inside the lockup. Watch what happens at the compliance peer.
peer chaincode invoke -C issuance-cre -n dr \
  -c '{"function":"TransferDR","Args":["CRE-BLR-001","INV-ALPHA","INV-GAMMA","5000"]}' \
  --tls --cafile "$ORDERER_CA" \
  --peerAddresses localhost:7051 --tlsRootCertFiles "$DEPOSITARY_CA" \
  --peerAddresses localhost:8051 --tlsRootCertFiles "$REGISTRAR_CA" \
  --peerAddresses localhost:10051 --tlsRootCertFiles "$COMPLIANCE_CA"
```

## What to look for on the network that the in-memory demo cannot show

1. **The refusal happens before ordering.** Query the block height before and
   after a refused transfer. It does not move. The transaction was never
   ordered, so there is nothing to revert.

2. **Private data stays private.** Run `GetPosition` from the sponsor's Lite
   Peer for a scheme it does not sponsor. It returns nothing, and that is the
   collection policy doing it, not application code.

3. **The Lite Peer never commits.** Watch the compliance peer's logs during a
   successful transfer. It endorses and stops. Its state database is not
   written, which is the whole point of the LP/CP split and the reason a
   distribution partner can join this network cheaply.

4. **The supervisor sees it live.** Subscribe to chaincode events on the
   observer node and watch `TransferRejected` and `ForceTransferExecuted`
   arrive as they happen.
