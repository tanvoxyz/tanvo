// Runs the evaluator's shared cases (../testdata/*.json, the ones the Go
// host and the Rust tests run) against the browser package, built for
// Node.js into target/node by `node ../scripts/build-js.mjs nodejs`.
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const require = createRequire(import.meta.url);
const { parseExpression, evaluateExpression } = require(join(here, '../target/node/tanvo_expression.js'));

// Cases the browser package cannot express. Its events carry property
// values as JavaScript strings and numbers, so a number property is never
// malformed: an over-long number text is a string, checked only when the
// expression reads it.
const skipped = new Map([
  ['a number property longer than 4096 bytes', 'a JavaScript event has no malformed number values'],
]);

// decimal returns the canonical text of a decimal ("1.50" and "15E-1" are
// "1.5"), so that results compare by value.
function decimal(text) {
  const m = /^([+-]?)(\d*)(?:\.(\d*))?(?:[eE]([+-]?\d+))?$/.exec(text.trim());
  assert.ok(m, `not a decimal: ${text}`);
  const [, sign, int, frac = '', exp = '0'] = m;
  let digits = (int + frac).replace(/^0+/, '');
  let scale = frac.length - Number(exp);
  if (digits === '') return '0';
  while (scale > 0 && digits.endsWith('0')) {
    digits = digits.slice(0, -1);
    scale--;
  }
  if (scale <= 0) return (sign === '-' ? '-' : '') + digits + '0'.repeat(-scale);
  const padded = digits.padStart(scale + 1, '0');
  return `${sign === '-' ? '-' : ''}${padded.slice(0, -scale)}.${padded.slice(-scale)}`;
}

// properties converts a case's properties: {"number": "text"} is a number
// given as text, kept as text unless it names a value that is not finite
// ("Infinity", "NaN"), which only a JavaScript number carries.
function properties(props) {
  return Object.fromEntries(
    Object.entries(props).map(([k, v]) => {
      if (v !== null && typeof v === 'object') {
        return [k, /^[+-]?(Infinity|NaN)$/.test(v.number) ? Number(v.number) : v.number];
      }
      return [k, v];
    }),
  );
}

const files = readdirSync(join(here, '../../testdata')).filter((f) => f.endsWith('.json'));
for (const file of files) {
  const data = JSON.parse(readFileSync(join(here, '../../testdata', file), 'utf8'));
  for (const c of data.cases) {
    const name = `${file}: ${c.name}`;
    const reason = skipped.get(c.name);
    test(name, { skip: reason }, () => {
      if (c.source) test.diagnostic?.(`source: ${c.source}`);
      if (c.syntax_error) {
        assert.throws(
          () => parseExpression(c.expression),
          (e) => e instanceof Error && (c.syntax_error.contains ?? []).every((part) => e.message.includes(part)),
        );
        return;
      }
      const expression = parseExpression(c.expression);
      const ev = c.event ?? data.event;
      if (!ev) return;
      const evaluate = () => evaluateExpression(expression, ev.code, BigInt(ev.timestamp), properties(ev.properties));
      if (c.error) {
        assert.throws(evaluate, (e) => {
          assert.ok(e instanceof Error);
          if (c.error.kind !== 'invalid_event') assert.equal(e.message, c.error.message);
          return true;
        });
      } else if (c.number !== undefined) {
        assert.equal(decimal(evaluate()), decimal(c.number), `got = ${evaluate()}, want = ${c.number}`);
      } else if (c.string !== undefined) {
        assert.equal(evaluate(), c.string);
      }
    });
  }
}

test('properties must be strings or numbers', () => {
  const expression = parseExpression('event.properties.a');
  assert.equal(evaluateExpression(expression, 'code', 1n, { a: 2.3 }), '2.3');
  assert.equal(evaluateExpression(expression, 'code', 1n, { a: 12n }), '12');
  assert.throws(() => evaluateExpression(expression, 'code', 1n, { a: true }), /properties\.a: expected a string or a number/);
  assert.throws(() => evaluateExpression(expression, 'code', 1n, undefined), /Variable: a not found/);
});
