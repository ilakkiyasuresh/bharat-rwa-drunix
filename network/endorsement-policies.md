# Endorsement policies

The compliance controls in this system are not in the chaincode. They are in
this file.

That distinction is the architectural claim of the whole project. A rule
enforced by an endorsement policy cannot be bypassed by a bug in the
chaincode, because a transaction that does not collect the required signatures
never reaches the ordering service and never enters a block. A rule enforced
only in application code is a rule that holds until someone deploys a bad
version of that code.

Applied at deploy time with `--signature-policy` on
`peer lifecycle chaincode approveformyorg`, and per function with
state-based endorsement where the chaincode-level policy is not specific
enough.

## Organisations

| MSP ID | Organisation | Node type |
| --- | --- | --- |
| `CitiDepositaryMSP` | Depositary, custodian, issuer agent | Committing Peer + YugabyteDB |
| `RegistrarMSP` | SEBI-registered RTA, statutory register | Committing Peer + YugabyteDB |
| `TrusteeMSP` | Independent trustee | Committing Peer |
| `ComplianceMSP` | Compliance and screening service | Lite Peer |
| `SponsorAlphaMSP` | Asset sponsor / investment manager | Lite Peer |
| `ValuerMSP` | Registered valuer | Lite Peer |
| `RegulatorMSP` | SEBI / RBI supervision | Observer Committing Peer, non-endorsing |

## Policies by function

| Function | Policy | Why these signatures |
| --- | --- | --- |
| `CreateScheme` | `AND('CitiDepositaryMSP.peer','TrusteeMSP.peer','RegistrarMSP.peer')` | A programme cannot exist without a depositary to issue it, a trustee to hold for holders, and a registrar to maintain the register |
| `SetSchemeStatus` | `AND('CitiDepositaryMSP.peer','TrusteeMSP.peer')` | Opening or closing a programme to transfers is a fiduciary act |
| `RegisterInvestor` | `AND('CitiDepositaryMSP.peer','RegistrarMSP.peer')` | The registrar maintains the register of members; the depositary onboards |
| `IssueDR` | `AND('CitiDepositaryMSP.peer','TrusteeMSP.peer','RegistrarMSP.peer')` | The trustee's signature is what ties a minted receipt to units it has accepted into custody. Without it, the receipt is an assertion |
| **`TransferDR`** | **`AND('CitiDepositaryMSP.peer','RegistrarMSP.peer','ComplianceMSP.peer')`** | **The compliance signature is the control.** Compliance runs the same chaincode during endorsement; if its evaluation refuses, it does not sign, the policy is unsatisfied, and the transaction is never ordered |
| `ForceTransfer` | `AND('CitiDepositaryMSP.peer','TrusteeMSP.peer','RegistrarMSP.peer')` | Three organisations, none able to act alone, plus a recorded reason and a real-time event to the supervisor |
| `SetFrozen` | `AND('CitiDepositaryMSP.peer','TrusteeMSP.peer')` | Freezing a holder's property requires more than one party |
| `RenewEligibility` | `AND('CitiDepositaryMSP.peer','ComplianceMSP.peer')` | Compliance owns the attestation; the depositary records it |
| `RecordValuation` | `AND('ValuerMSP.peer','CitiDepositaryMSP.peer')` | The valuer signs because the valuation is theirs. A NAV the valuer did not sign is a NAV the platform made up |
| `RunDistribution` | `AND('CitiDepositaryMSP.peer','RegistrarMSP.peer')` | The registrar holds the entitlement record the computation runs against |
| `GetScheme`, `GetPosition` | Query only | No endorsement. Read access to holder detail is governed by the private data collection policy, not by this table |

## What the regulator node can and cannot do

`RegulatorMSP` is a member of every private data collection and an endorser of
nothing. It sees every transaction, every compliance refusal and every
privileged action as it happens, and it cannot cause any of them.

This replaces periodic reporting with continuous visibility. It should be in
the first conversation with the supervisor, not offered later as a
concession.

## Why no single organisation controls ordering

Five Raft orderers: two at the depositary, two at the registrar, one at the
trustee. No organisation holds a majority, and channel configuration changes
require a majority across at least two organisations.

If the depositary ran every orderer, the network would be that
organisation's database with extra latency and no counterparty would have a
reason to join it. Distributed ordering is what makes this a network rather
than a hosted service, and it belongs in the participation agreement rather
than only in a config file that somebody can later change.

## Applying a policy

```bash
peer lifecycle chaincode approveformyorg \
  --channelID issuance-cre \
  --name dr --version 1.0 --sequence 1 \
  --signature-policy "AND('CitiDepositaryMSP.peer','RegistrarMSP.peer','ComplianceMSP.peer')" \
  --collections-config ../network/collections_config.json \
  --package-id "$PACKAGE_ID" \
  --orderer "$ORDERER" --tls --cafile "$ORDERER_CA"
```

Per-function granularity beyond the chaincode-level policy is applied with
state-based endorsement on the relevant key prefixes, set by the chaincode at
key creation via `SetStateValidationParameter`.

> **Status.** These policies are designed and documented; they are applied and
> verified when the seven-organisation network is stood up. See the "Built,
> designed, not done" table in the top-level README for exactly what has been
> executed and what has not.
