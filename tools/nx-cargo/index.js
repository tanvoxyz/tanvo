// Local Nx plugin that turns every Cargo workspace (a Cargo.toml with a
// [workspace] table) into an Nx project with standard targets. A workspace
// names the Nx projects it uses outside Cargo, such as a project whose
// generated Rust it includes, in [workspace.metadata.nx]:
//
//   implicitDependencies = ["proto"]
const { readFileSync } = require('node:fs');
const { join } = require('node:path');
// Nx identifies files by workspace-relative paths with forward slashes on every
// platform, so project roots are built with path.posix.
const { basename, dirname } = require('node:path').posix;

const KINDS = { apps: 'app', libs: 'lib', providers: 'provider', ee: 'ee', tools: 'tool' };

function run(command, root, extra = {}) {
  return { executor: 'nx:run-commands', options: { command, cwd: root }, ...extra };
}

function cargoTargets(root) {
  const cached = { cache: true, inputs: ['rust', '^rustGen'] };
  return {
    build: run('cargo build --locked --all-targets', root, cached),
    test: run('cargo test --locked', root, cached),
    bench: run('cargo bench --locked', root, { cache: false }),
    lint: run('cargo fmt --check && cargo clippy --locked --all-targets -- -D warnings', root, cached),
  };
}

// The implicitDependencies list of [workspace.metadata.nx], read without a
// TOML parser: the table holds only this key.
function implicitDependencies(manifest) {
  const table = manifest.match(/^\[workspace\.metadata\.nx\]\n((?:(?!\[).*\n?)*)/m);
  const list = table?.[1].match(/^implicitDependencies\s*=\s*\[([^\]]*)\]/m);
  return list ? [...list[1].matchAll(/"([^"]+)"/g)].map((m) => m[1]) : [];
}

const createNodesFn = async (configFiles, _options, context) =>
  configFiles
    .filter((file) => !file.includes('node_modules/') && !file.includes('target/'))
    .flatMap((file) => {
      const manifest = readFileSync(join(context.workspaceRoot, file), 'utf8');
      if (!/^\[workspace\]/m.test(manifest)) {
        return [];
      }
      const root = dirname(file);
      const kind = KINDS[root.split('/')[0]] ?? 'other';
      return [
        [
          file,
          {
            projects: {
              [root]: {
                name: basename(root),
                root,
                projectType: kind === 'lib' ? 'library' : 'application',
                tags: [`type:${kind}`, 'lang:rust'],
                implicitDependencies: implicitDependencies(manifest),
                targets: cargoTargets(root),
              },
            },
          },
        ],
      ];
    });

const createNodesV2 = ['**/Cargo.toml', createNodesFn];

module.exports = { createNodes: createNodesV2, createNodesV2 };
