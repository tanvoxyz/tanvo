// Builds the browser package of the evaluator (js/): the release WASM
// module of the wasm-bindgen crate, bound by wasm-bindgen-cli (pinned in
// mise.toml) for the target given as the first argument: "web" (the
// default) writes the ES module package to js/pkg, the one the web UI
// imports; "nodejs" writes the CommonJS package the tests load to
// js/target/node.
//
// The source paths the module keeps in its panic locations are remapped, so
// that it does not depend on where this repository or the Cargo home is.
import { execFileSync } from 'node:child_process';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const crate = join(root, 'js');
const target = process.argv[2] ?? 'web';
const outDir = { web: join(crate, 'pkg'), nodejs: join(crate, 'target/node') }[target];
if (!outDir) {
  throw new Error(`unknown target ${target}: expected web or nodejs`);
}
const cargoHome = process.env.CARGO_HOME ?? join(homedir(), '.cargo');
const rustflags = [
  process.env.RUSTFLAGS,
  `--remap-path-prefix=${join(cargoHome, 'registry/src')}=/cargo/registry/src`,
  `--remap-path-prefix=${root}=/tanvo/libs/expression`,
]
  .filter(Boolean)
  .join(' ');

execFileSync('cargo', ['build', '--locked', '--release', '--lib', '--target', 'wasm32-unknown-unknown'], {
  cwd: crate,
  stdio: 'inherit',
  env: { ...process.env, RUSTFLAGS: rustflags },
});
execFileSync(
  'wasm-bindgen',
  [
    '--target',
    target,
    '--out-dir',
    outDir,
    '--out-name',
    'tanvo_expression',
    join(crate, 'target/wasm32-unknown-unknown/release/tanvo_expression_js.wasm'),
  ],
  { cwd: crate, stdio: 'inherit' },
);
