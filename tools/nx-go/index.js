// Local Nx plugin that turns every Go module (go.mod) into an Nx project with
// standard targets, and derives the project graph from in-repo `require`
// directives in go.mod.
const { existsSync, readFileSync } = require('node:fs');
const { join } = require('node:path');
// Nx identifies files by workspace-relative paths with forward slashes on every
// platform, so project roots and source files are built with path.posix.
const { basename, dirname, join: joinPosix } = require('node:path').posix;

const MODULE_PREFIX = 'github.com/tanvoxyz/tanvo/';

const KINDS = { apps: 'app', libs: 'lib', providers: 'provider', ee: 'ee', tools: 'tool' };

function run(command, root, extra = {}) {
  const { env, ...rest } = extra;
  return {
    executor: 'nx:run-commands',
    options: { command, cwd: root, ...(env ? { env } : {}) },
    ...rest,
  };
}

function goTargets(root, workspaceRoot) {
  const cached = { cache: true, inputs: ['go', '^go'] };
  const targets = {
    build: run('go build ./...', root, { ...cached, env: { CGO_ENABLED: '0' } }),
    test: run('go test ./...', root, { ...cached, inputs: ['go', 'goTestdata', '^go'] }),
    bench: run('go test -run=NONE -bench=. -benchmem ./...', root, { cache: false }),
    lint: run('golangci-lint run --allow-parallel-runners ./...', root, { ...cached, inputs: ['go', 'goLintConfig'] }),
    'tidy-check': run('go mod tidy -diff', root, {
      cache: true,
      inputs: ['go'],
      env: { GOWORK: 'off' },
    }),
  };
  if (existsSync(join(workspaceRoot, root, 'buf.yaml'))) {
    targets.generate = run('buf generate', root, {
      cache: false,
      outputs: ['{projectRoot}/gen'],
    });
    targets['buf-lint'] = run('buf lint && buf format --diff --exit-code', root, {
      cache: true,
      inputs: ['{projectRoot}/**/*.proto', '{projectRoot}/buf.yaml'],
    });
  }
  return targets;
}

const createNodesFn = async (configFiles, _options, context) =>
  configFiles
    .filter((file) => !file.includes('node_modules/'))
    .map((file) => {
      const root = dirname(file);
      const top = root.split('/')[0];
      const kind = KINDS[top] ?? 'other';
      // A Go module named go/ beside other sources, such as the Go host of
      // a Rust library, is named after its parent: libs/expression/go is
      // expression-go.
      const name = basename(root) === 'go' ? `${basename(dirname(root))}-go` : basename(root);
      return [
        file,
        {
          projects: {
            [root]: {
              name,
              root,
              projectType: kind === 'lib' ? 'library' : 'application',
              tags: [`type:${kind}`, 'lang:go'],
              targets: goTargets(root, context.workspaceRoot),
            },
          },
        },
      ];
    });

const createNodesV2 = ['**/go.mod', createNodesFn];

const REQUIRE_RE = new RegExp(`^\\s*(?:require\\s+)?(${MODULE_PREFIX.replaceAll('.', '\\.')}\\S+)\\s+v`, 'gm');

function createDependencies(_options, context) {
  const byModule = new Map();
  for (const [key, project] of Object.entries(context.projects)) {
    const root = project.root ?? key;
    if (existsSync(join(context.workspaceRoot, root, 'go.mod'))) {
      byModule.set(MODULE_PREFIX + root, project.name ?? key);
    }
  }

  const deps = [];
  for (const [modulePath, source] of byModule) {
    const root = modulePath.slice(MODULE_PREFIX.length);
    const goMod = readFileSync(join(context.workspaceRoot, root, 'go.mod'), 'utf8');
    for (const [, required] of goMod.matchAll(REQUIRE_RE)) {
      const target = byModule.get(required);
      if (target && target !== source) {
        deps.push({ source, target, type: 'static', sourceFile: joinPosix(root, 'go.mod') });
      }
    }
  }
  return deps;
}

module.exports = { createNodes: createNodesV2, createNodesV2, createDependencies };
