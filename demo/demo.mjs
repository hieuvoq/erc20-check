// Deploy genuine Uniswap V2 contracts to a private chain and fork it for every scan.
import assert from "node:assert/strict";
import { spawn, execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import net from "node:net";
import { fileURLToPath } from "node:url";
import { setTimeout as delay } from "node:timers/promises";
import solc from "solc";
import {
  ContractFactory,
  JsonRpcProvider,
  MaxUint256,
  parseEther,
} from "ethers";

const dir = fileURLToPath(new URL(".", import.meta.url));
process.chdir(join(dir, ".."));
const tmp = mkdtempSync(join(tmpdir(), "erc20-demo-"));
const artifact = (pkg, name) =>
  JSON.parse(
    readFileSync(
      join(dir, "node_modules/@uniswap", pkg, "build", name + ".json"),
    ),
  );
const sock = net.createServer();
await new Promise((resolve) => sock.listen(0, "127.0.0.1", resolve));
const port = sock.address().port;
await new Promise((resolve) => sock.close(resolve));
const rpc = "http://127.0.0.1:" + port;
const child = spawn(
  "anvil",
  [
    "--port",
    String(port),
    "--host",
    "127.0.0.1",
    "--chain-id",
    "31337",
    "--silent",
    "--mnemonic-random",
    "12",
  ],
  { stdio: "ignore" },
);
const provider = new JsonRpcProvider(rpc, 31337, {
  staticNetwork: true,
  cacheTimeout: -1,
});
provider.pollingInterval = 50;
try {
  for (let i = 0; i < 100; i++) {
    if (child.exitCode !== null) throw Error("fixture node exited");
    try {
      await provider.send("eth_chainId", []);
      break;
    } catch {
      await delay(60);
    }
  }
  const signer = await provider.getSigner(0);
  const owner = await signer.getAddress();
  async function deploy(abi, bytecode, ...args) {
    const contract = await new ContractFactory(abi, bytecode, signer).deploy(
      ...args,
    );
    await contract.waitForDeployment();
    return contract;
  }
  async function published(pkg, name, ...args) {
    const a = artifact(pkg, name);
    return deploy(a.abi, a.bytecode, ...args);
  }
  const src = readFileSync(join(dir, "FixtureToken.sol"), "utf8");
  const compiled = JSON.parse(
    solc.compile(
      JSON.stringify({
        language: "Solidity",
        sources: { "FixtureToken.sol": { content: src } },
        settings: {
          outputSelection: { "*": { "*": ["abi", "evm.bytecode.object"] } },
        },
      }),
    ),
  );
  const errors = (compiled.errors ?? []).filter((e) => e.severity === "error");
  assert.deepEqual(errors, []);
  const f = compiled.contracts["FixtureToken.sol"].FixtureToken;
  const token = await deploy(f.abi, f.evm.bytecode.object);
  const quote = await deploy(f.abi, f.evm.bytecode.object);
  const weth = await published("v2-periphery", "WETH9");
  const factory = await published("v2-core", "UniswapV2Factory", owner);
  const router = await published(
    "v2-periphery",
    "UniswapV2Router02",
    factory.target,
    weth.target,
  );
  const mine = async (tx) => {
    await (await tx).wait();
  };
  for (const asset of [token, quote]) {
    await mine(asset.approve(router.target, MaxUint256));
    await mine(
      router.addLiquidityETH(
        asset.target,
        parseEther("10000"),
        0,
        0,
        owner,
        MaxUint256,
        { value: parseEther("10") },
      ),
    );
  }
  await mine(
    router.addLiquidity(
      token.target,
      quote.target,
      parseEther("10000"),
      parseEther("10000"),
      0,
      0,
      owner,
      MaxUint256,
    ),
  );
  const pairA = await factory.getPair(token.target, weth.target);
  const pairB = await factory.getPair(token.target, quote.target);
  await mine(token.configure(pairA, 0, 300, 0));
  const exe = join(tmp, "erc20-check");
  execFileSync("go", ["build", "-o", exe, "./cmd/erc20-check"]);
  function scan(pair, other) {
    const args = [
      "-rpc",
      rpc,
      "-chain",
      "31337",
      "-token",
      token.target,
      "-fixture-router",
      router.target,
      "-pool",
      pair,
      "-block",
      String(height),
    ];
    if (other) args.push("-quote", other);
    const report = JSON.parse(
      execFileSync(exe, args, { encoding: "utf8", maxBuffer: 4 << 20 }),
    );
    return report;
  }
  let height = await provider.getBlockNumber();
  const fees = (r) =>
    ["buy", "sell_partial", "sell_all", "transfer"].map(
      (k) => r.legs[k].tax_bps,
    );
  for (const label of ["A", "B", "B", "A"]) {
    const a = label === "A";
    const r = scan(a ? pairA : pairB, a ? null : quote.target);
    assert.deepEqual(fees(r), a ? [0, 300, 300, 0] : [0, 0, 0, 0]);
    assert.equal(r.markLevel, a ? "NOTICE" : "NORMAL");
    console.log("pool", label, r.markLevel, fees(r));
  }
  await mine(token.configure(pairA, 200, 300, 100));
  height = await provider.getBlockNumber();
  const mixed = scan(pairA, null);
  assert.deepEqual(fees(mixed), [200, 300, 300, 100]);
  assert.equal(mixed.markLevel, "NOTICE");
  console.log("positive buy/sell/wallet tax", fees(mixed));
  assert.equal(await token.balanceOf(mixed.fork_wallet), 0n);
  await mine(token.setDelegatedLimit(BigInt(mixed.legs.buy.received_raw) / 2n));
  height = await provider.getBlockNumber();
  const limited = scan(pairA, null);
  assert.equal(limited.legs.sell_partial.status, "ok");
  assert.equal(limited.legs.sell_all.status, "reverted");
  assert.equal(limited.legs.sell_all.tax_bps, null);
  assert.equal(limited.markLevel, "WARNING");
  console.log(
    "delegated transfer cap: partial sale ok, full sale reverted, WARNING",
  );
  const previous = join(tmp, "previous.json");
  writeFileSync(previous, JSON.stringify(mixed));
  // An unavailable upstream must preserve the earlier finding as historical evidence.
  const { spawnSync } = await import("node:child_process");
  const offline = spawnSync(
    exe,
    [
      "-chain",
      "31337",
      "-fixture-router",
      router.target,
      "-token",
      token.target,
      "-pool",
      pairA,
      "-rpc",
      "http://127.0.0.1:1",
      "-previous",
      previous,
    ],
    { encoding: "utf8" },
  );
  assert.equal(offline.status, 3);
  const missing = JSON.parse(offline.stdout);
  assert.equal(missing.markLevel, "UNKNOWN");
  assert.equal(missing.previous.markLevel, "NOTICE");
  console.log("RPC offline: UNKNOWN, previous NOTICE");
} finally {
  provider.destroy();
  child.kill();
  rmSync(tmp, { recursive: true, force: true });
}
