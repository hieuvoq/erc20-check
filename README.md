# erc20-check

Measure what a wallet actually receives when it buys, sells, or transfers an ERC-20. The Go CLI runs swaps on a disposable Ethereum mainnet fork using Uniswap V2 Router02, compares the observations with GoPlus, and emits a JSON report for one pool, block and trade size.

## Try it

Install Go 1.22+ and Foundry's `anvil` on macOS or Linux. The Go binary uses the standard library. Provide an Ethereum RPC that serves the selected block's state.

```sh
make build
export ETH_RPC_URL=https://ethereum-rpc.publicnode.com
./erc20-check -token 0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48 > usdc.json
jq '{block, pool, markLevel, reasons, legs, goplus}' usdc.json
```

A recorded run at block **26,505,256** spent 0.01 fork ETH and received **17.270304 USDC**. Selling half and selling all succeeded. Each of the four measured legs had **0 bps** of token tax. GoPlus returned zero buy and sell tax and supplied no wallet-transfer tax value. [Full JSON evidence](examples/usdc-mainnet.json) · [Ethereum block](https://etherscan.io/block/26505256)

| Option | Effect |
| --- | --- |
| `-amount-wei N` | Native input in raw wei, default 10000000000000000. The fork wraps it into WETH. |
| `-quote ADDRESS` | Test the target/quote pool using the path WETH → quote → target. Defaults to the direct WETH pair. |
| `-pool ADDRESS` | Require the factory pair for the selected quote to match this address. |
| `-block N` | Pin an explicit height. The default resolves the finalized block, then verifies the fork hash. |
| `-warning-bps N` | Inclusive fee warning threshold. Default 1000 bps (10%). |
| `-previous FILE` | Supply a trusted earlier report to retain historical evidence or confirm a recurring restriction on the same market. |
| `-no-goplus` | Skip the provider lookup. Set `GOPLUS_TOKEN` for optional bearer authentication. |

The CLI writes transactions only through a private Unix socket to its own Anvil child. The child uses fresh funded accounts and exits after the scan. Fork transaction hashes refer to that simulation. Use `-block` to repeat the market snapshot. New scans use a fresh wallet and can observe different behavior for address-dependent tokens.

## Mainnet example: why FLOKI received `NOTICE`

At Ethereum block **26,505,320**, the CLI spent **0.01 fork ETH** buying FLOKI through the FLOKI/WETH pair. The pool sent **1,022,599.511991790 FLOKI** and the wallet received **1,019,531.713455815 FLOKI**. The difference of **3,067.798535975 FLOKI** is the measured buy fee. [Pool](https://etherscan.io/address/0xca7c2771d248dcbe09eabe0ce57a62e18da178c0) · [Block](https://etherscan.io/block/26505320) · [Recorded JSON](examples/floki-mainnet.json)

| Leg | FLOKI debited | FLOKI credited to recipient | Local tax | GoPlus tax |
| --- | ---: | ---: | ---: | ---: |
| Buy | 1,022,599.511991790 | 1,019,531.713455815 | ≈0.30% | 0.30% |
| Sell half | 509,765.856727907 | 508,236.559157724 | ≈0.30% | 0.30% |
| Sell all | 1,019,531.713455815 | 1,016,473.118315448 | ≈0.30% | 0.30% |
| Wallet transfer | 509,765.856727907 | 509,765.856727907 | 0% | 0% |

Each sell receipt contains two swaps from the same pair and a liquidity addition. On the full sale the pool gained **1,503,208.226265433 FLOKI** across the entire transaction. The seller contributed **1,016,473.118315448 FLOKI** to the swap that paid WETH to the test wallet. The JSON records the selected swap's zero-based receipt offset and the total pool change for both sales.

**Why `NOTICE`?** Both sells succeeded. The measured buy and sell fees are just under 30 bps, which the CLI floors to 29 whole bps. GoPlus reported 30 bps for both and zero for transfers when queried at **2026-11-27 09:15:55 UTC**. The difference is within the 1 bps comparison tolerance, and the fees are below the 1,000 bps warning threshold. All tested legs completed, giving the behavior `fee_on_transfer`.

To repeat the simulation, use an Ethereum RPC with historical state at this block:

```sh
./erc20-check -token 0xcf0c122c6b73ff809c693db761e7baebe62b6a2e \
  -pool 0xca7c2771d248dcbe09eabe0ce57a62e18da178c0 \
  -block 26505320 -amount-wei 10000000000000000
```

## How the measurements work

1. Resolve the canonical factory pair for the selected Router02 route. Record chain, block hash, pool, path, wallet and input. Check a reference router quote before each trade.
2. Buy the token with Router02's fee-on-transfer-compatible swap. Read the target pair's nominal output from its Swap event, verify the pool's token debit, and measure the recipient's balance increase.
3. From the post-buy wallet balance, test an ordinary wallet-to-wallet transfer. Approve the router and read back the allowance. Sell half and sell the entire position in separate snapshots of the same approved state. Each successful sale must credit the wallet with wrapped native proceeds.
4. For a single-swap sale, verify the pool balance delta against the swap input. For multiple swaps on the target pair, identify a unique swap by the router, output recipient and trade direction. Match its input to wallet-to-pool transfers in that reserve interval (bounded by V2 `Sync` events), and reconcile all target-token transfers involving the pool with its observed balance change. Ambiguous or inconsistent evidence leaves the sell tax unknown. For a wallet transfer, compare the nominal amount with both wallet balance changes.

For a measurable recipient-side tax, `tax_bps = floor(10000 × (nominal − received) / nominal)`. Raw amounts preserve the precision hidden by integer bps. On a sale, `received_raw` is the **target token credited to the pool**. `actual_out_raw` is the **WETH received by the seller**. On a buy, `actual_out_raw` is the purchased token. The report also records `quoted_out_raw` in the same output asset. This keeps the token tax separate from pool fees and price impact.

A missing pool, unavailable state, ambiguous balance change or failed measurement leaves the local tax null. A reverted full sale has a separate status from a successful sale with a measured fee. The CLI covers one V2 route and the chosen amounts. Other pools, wallet policies, token rebases and trading cooldowns can yield different results.

## Marks and evidence

| markLevel | Rule |
| --- | --- |
| NORMAL | Buy, approval, both sales and wallet transfer succeeded with full token credit. |
| NOTICE | A recipient-side fee was measured below the warning threshold. Partial coverage is labeled explicitly. |
| WARNING | A tested operation reverted, tax reached the configured threshold, or comparable GoPlus and local evidence disagree. |
| CRITICAL | In two supplied observations at different increasing block heights on the same market and input, the token could be bought and transferred to another wallet, both sales and a direct transfer to the pool reverted, and a separate V2 control round trip succeeded. |
| UNKNOWN | The run lacks adequate local evidence. An earlier report supplied with `-previous` can still be displayed as historical evidence. |

One report can contain both a measured problem and an untested leg. Read `legs`, `confidence`, `reasons` and any `probe_error` along with the mark. CRITICAL describes a repeated sell restriction under these conditions. The live control uses the mainnet USDC/WETH pair, or DAI/WETH when USDC is the target.

GoPlus observations have their own retrieval timestamp. The service supplies token-level results at query time, which may cover a different pool or block. The report keeps available raw fields, including mutable-tax and pause flags. Missing or empty provider tax stays unknown. A provider tax of 1 means a reported fraction of 100% and may also reflect a blocked simulation. Only a local measured balance change sets a local tax. A difference greater than one basis point flags a tax disagreement. [GoPlus response fields](https://docs.gopluslabs.io/reference/response-details) · [Uniswap V2 deployments](https://developers.uniswap.org/docs/protocols/v2/deployments)

Exit codes are 0 for a completed assessment including warnings, 2 for invalid CLI inputs or output errors, and 3 for UNKNOWN. CI can apply its own gating policy to the JSON.

## Reproduce the demo

```sh
make test
make demo
```

The demo also needs Node.js 20+ and npm. It deploys the published Uniswap V2 factory, Router02 and WETH9 artifacts to a temporary local chain, creates two pools for the same fixture token and runs the Go binary against each fork. No external RPC or GoPlus credential is required for this demo. npm downloads the pinned fixture dependencies on setup.

| Fixture | Buy | Sell half | Sell all | Wallet transfer | Mark |
| --- | ---: | ---: | ---: | ---: | --- |
| Pool A charges a sell fee | 0 | 300 bps | 300 bps | 0 | NOTICE |
| Same token in pool B | 0 | 0 | 0 | 0 | NORMAL |
| Three independent fees | 200 bps | 300 bps | 300 bps | 100 bps | NOTICE |
| Delegated amount capped at half the position | 200 bps | 300 bps | Reverted | 100 bps | WARNING |

The script checks both pool orders, a full-sale failure and an unavailable RPC after a previous successful scan. Go tests cover mark escalation, ambiguous balances, RPC errors and provider disagreements. The public mainnet report and the controlled fixture exercise different evidence paths.
