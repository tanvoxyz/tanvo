// Builds the release WASM module and copies it to go/expression.wasm, which
// the Go host embeds. The copy is committed, so that Go builds do not need
// Rust.
//
// The module is built in a pinned linux/amd64 Rust container, so that every
// machine produces the same bytes and CI can check the committed copy:
// Cargo folds the host triple into each crate's metadata hash, which moves
// symbols and data after LTO, so a native build on another host differs.
// TANVO_WASM_BUILD=native builds with the local toolchain instead, for
// machines without Docker; its output is not the committed one.
//
// The source paths the module keeps in its panic locations are remapped, so
// that it does not depend on where this repository or the Cargo home is.
import { execFileSync } from 'node:child_process';
import { chmodSync, copyFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

// IMAGE is rust:1.99.0 for linux/amd64, matching rust-toolchain.toml.
const IMAGE = 'rust:1.99.0@sha256:d3ba1ad5d5b5a180ca8428b4fab8543a1b3933b32a8a7ad90a7ebbaf5497203a';
// The Docker volumes keeping the toolchain (with the wasm32 target added
// once) and the downloaded crates between builds.
const RUSTUP_VOLUME = 'tanvo-wasm-rustup';
const REGISTRY_VOLUME = 'tanvo-wasm-cargo-registry';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const cargoArgs = ['build', '--locked', '--release', '--lib', '--target', 'wasm32-unknown-unknown'];

function rustflags(cargoHome, source) {
  return [
    process.env.RUSTFLAGS,
    `--remap-path-prefix=${join(cargoHome, 'registry/src')}=/cargo/registry/src`,
    `--remap-path-prefix=${source}=/tanvo/libs/expression`,
  ]
    .filter(Boolean)
    .join(' ');
}

let built;
if (process.env.TANVO_WASM_BUILD === 'native') {
  const cargoHome = process.env.CARGO_HOME ?? join(homedir(), '.cargo');
  execFileSync('cargo', cargoArgs, {
    cwd: root,
    stdio: 'inherit',
    env: { ...process.env, RUSTFLAGS: rustflags(cargoHome, root) },
  });
  built = join(root, 'target/wasm32-unknown-unknown/release/tanvo_expression.wasm');
} else {
  // The container's own target directory keeps its artifacts apart from
  // native builds. It runs as root to add the wasm32 target to its
  // toolchain volume, then hands the artifacts back to the caller.
  const target = 'target/docker';
  const owner = `${process.getuid()}:${process.getgid()}`;
  const script = [
    'rustup target add wasm32-unknown-unknown >/dev/null',
    'cargo "$@"',
    'status=$?',
    `chown -R ${owner} /work/${target}`,
    'exit $status',
  ].join('\n');
  execFileSync(
    'docker',
    [
      'run', '--rm', '--platform', 'linux/amd64',
      '-v', `${root}:/work`, '-w', '/work',
      '-v', `${RUSTUP_VOLUME}:/usr/local/rustup`,
      '-v', `${REGISTRY_VOLUME}:/usr/local/cargo/registry`,
      '-e', 'RUSTUP_TOOLCHAIN=1.99.0',
      '-e', `CARGO_TARGET_DIR=/work/${target}`,
      '-e', `RUSTFLAGS=${rustflags('/usr/local/cargo', '/work')}`,
      IMAGE,
      'sh', '-c', script, 'cargo',
      ...cargoArgs,
    ],
    { cwd: root, stdio: 'inherit' },
  );
  built = join(root, target, 'wasm32-unknown-unknown/release/tanvo_expression.wasm');
}

const to = join(root, 'go/expression.wasm');
copyFileSync(built, to);
// The linker marks its output executable; the committed file is data.
chmodSync(to, 0o644);
