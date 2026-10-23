// Builds the release WASM module and copies it to go/expression.wasm, which
// the Go host embeds. The copy is committed, so that Go builds do not need
// Rust.
//
// The source paths the module keeps in its panic locations are remapped, so
// that it does not depend on where this repository or the Cargo home is.
import { execFileSync } from 'node:child_process';
import { chmodSync, copyFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const cargoHome = process.env.CARGO_HOME ?? join(homedir(), '.cargo');
const rustflags = [
  process.env.RUSTFLAGS,
  `--remap-path-prefix=${join(cargoHome, 'registry/src')}=/cargo/registry/src`,
  `--remap-path-prefix=${root}=/tanvo/libs/expression`,
]
  .filter(Boolean)
  .join(' ');

execFileSync('cargo', ['build', '--locked', '--release', '--lib', '--target', 'wasm32-unknown-unknown'], {
  cwd: root,
  stdio: 'inherit',
  env: { ...process.env, RUSTFLAGS: rustflags },
});

const to = join(root, 'go/expression.wasm');
copyFileSync(join(root, 'target/wasm32-unknown-unknown/release/tanvo_expression.wasm'), to);
// The linker marks its output executable; the committed file is data.
chmodSync(to, 0o644);
