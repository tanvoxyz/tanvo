//! Runs the cases of testdata/*.json through the WASM module's interface
//! ([`tanvo_expression::abi`]); the Go host's tests run the same files
//! through the built module.
//!
//! A file holds a `description`, an optional default `event` and `cases`.
//! A case has a `source` (the upstream spec it is ported from, omitted for a
//! case of Tanvo's own, such as its limits), a `name`, an `expression`, an
//! optional `event` and one expectation:
//!
//! * `"valid": true`: the expression parses;
//! * `"syntax_error": {kind, line?, column?, offset?, message?, contains?}`:
//!   it does not, `kind` being `syntax`, `arguments`, `decimal`, `nesting`
//!   or `length`;
//! * `"number": "12.5"`: the event evaluates to that number, printed as the
//!   module prints it;
//! * `"string": "text"`: the event evaluates to that string;
//! * `"error": {kind, message}`: the event's evaluation fails.
//!
//! An event is `{code, timestamp, properties}`; a property is a JSON string,
//! a JSON number, or `{"number": "text"}` for a number JSON cannot write.

use std::{fs, path::Path};

use serde::Deserialize;
use serde_json::Value;

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct File {
    #[allow(dead_code)]
    description: String,
    event: Option<Value>,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Case {
    source: Option<String>,
    name: String,
    #[allow(dead_code)]
    note: Option<String>,
    expression: String,
    event: Option<Value>,
    #[serde(default)]
    valid: bool,
    syntax_error: Option<SyntaxError>,
    number: Option<String>,
    string: Option<String>,
    error: Option<EvalError>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct SyntaxError {
    kind: String,
    line: Option<u32>,
    column: Option<u32>,
    offset: Option<u32>,
    message: Option<String>,
    #[serde(default)]
    contains: Vec<String>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct EvalError {
    kind: String,
    message: String,
}

fn put_str(b: &mut Vec<u8>, s: &str) {
    b.extend_from_slice(&(s.len() as u32).to_le_bytes());
    b.extend_from_slice(s.as_bytes());
}

fn put_value(b: &mut Vec<u8>, v: &Value) {
    match v {
        Value::String(s) => {
            b.push(0);
            put_str(b, s);
        }
        Value::Number(n) => {
            b.push(1);
            put_str(b, &n.to_string());
        }
        Value::Object(o) => {
            b.push(1);
            put_str(b, o["number"].as_str().expect("a {\"number\": text} value"));
        }
        v => panic!("unsupported value {v}"),
    }
}

fn put_event(b: &mut Vec<u8>, e: &Value) {
    put_str(b, e["code"].as_str().expect("a code"));
    put_value(b, &e["timestamp"]);
    let props = e["properties"].as_object().expect("properties");
    b.extend_from_slice(&(props.len() as u32).to_le_bytes());
    for (k, v) in props {
        put_str(b, k);
        put_value(b, v);
    }
}

struct Reader<'a>(&'a [u8]);

impl<'a> Reader<'a> {
    fn u8(&mut self) -> u8 {
        let v = self.0[0];
        self.0 = &self.0[1..];
        v
    }

    fn u32(&mut self) -> u32 {
        let (h, t) = self.0.split_at(4);
        self.0 = t;
        u32::from_le_bytes(h.try_into().unwrap())
    }

    fn str(&mut self) -> &'a str {
        let n = self.u32() as usize;
        let (h, t) = self.0.split_at(n);
        self.0 = t;
        std::str::from_utf8(h).unwrap()
    }
}

const PARSE_KINDS: [&str; 5] = ["syntax", "arguments", "decimal", "nesting", "length"];
const ERROR_KINDS: [&str; 7] = [
    "empty_argument_list",
    "expected_decimal",
    "missing_variable",
    "division_by_zero",
    "invalid_event",
    "digits_out_of_range",
    "number_out_of_range",
];

fn check(file: &File, c: &Case) -> Result<(), String> {
    let event = c.event.as_ref().or(file.event.as_ref());
    let evaluates = c.number.is_some() || c.string.is_some() || c.error.is_some();
    let mut req = Vec::new();
    put_str(&mut req, &c.expression);
    match (evaluates, event) {
        (true, Some(e)) => {
            req.extend_from_slice(&1u32.to_le_bytes());
            put_event(&mut req, e);
        }
        (true, None) => return Err("an evaluation case without an event".into()),
        (false, _) => req.extend_from_slice(&0u32.to_le_bytes()),
    }
    let resp = tanvo_expression::abi::evaluate(&req).ok_or("malformed request")?;
    let mut r = Reader(&resp);
    if r.u8() != 0 {
        let kind = PARSE_KINDS[r.u8() as usize];
        let (line, column, offset) = (r.u32(), r.u32(), r.u32());
        let message = r.str();
        let Some(want) = &c.syntax_error else {
            return Err(format!("got = parse error {message:?}, want = it to parse"));
        };
        if kind != want.kind {
            return Err(format!("got = kind {kind}, want = {}", want.kind));
        }
        for (name, got, want) in [
            ("line", line, want.line),
            ("column", column, want.column),
            ("offset", offset, want.offset),
        ] {
            if let Some(want) = want
                && got != want
            {
                return Err(format!("got = {name} {got}, want = {want}"));
            }
        }
        if let Some(want) = &want.message
            && message != want
        {
            return Err(format!("got = message {message:?}, want = {want:?}"));
        }
        for part in &want.contains {
            if !message.contains(part.as_str()) {
                return Err(format!(
                    "got = message {message:?}, want = it to contain {part:?}"
                ));
            }
        }
        return Ok(());
    }
    if c.syntax_error.is_some() {
        return Err("got = it parses, want = a syntax error".into());
    }
    if !evaluates {
        return if c.valid {
            Ok(())
        } else {
            Err("a case without an expectation".into())
        };
    }
    assert_eq!(r.u32(), 1);
    let got = match r.u8() {
        0 => format!("number {:?}", r.str()),
        1 => format!("string {:?}", r.str()),
        _ => {
            let kind = ERROR_KINDS[r.u8() as usize];
            format!("error {kind} {:?}", r.str())
        }
    };
    let want = match (&c.number, &c.string, &c.error) {
        (Some(n), None, None) => format!("number {n:?}"),
        (None, Some(s), None) => format!("string {s:?}"),
        (None, None, Some(e)) => format!("error {} {:?}", e.kind, e.message),
        _ => return Err("a case with several expectations".into()),
    };
    if got != want {
        return Err(format!("got = {got}, want = {want}"));
    }
    Ok(())
}

#[test]
fn testdata_cases() {
    let dir = Path::new(env!("CARGO_MANIFEST_DIR")).join("testdata");
    let mut paths: Vec<_> = fs::read_dir(&dir)
        .unwrap()
        .map(|e| e.unwrap().path())
        .filter(|p| p.extension().is_some_and(|x| x == "json"))
        .collect();
    paths.sort();
    assert!(!paths.is_empty(), "no testdata in {}", dir.display());
    let mut failures = Vec::new();
    let mut count = 0;
    for path in paths {
        let file: File = serde_json::from_str(&fs::read_to_string(&path).unwrap())
            .unwrap_or_else(|e| panic!("{}: {e}", path.display()));
        for c in &file.cases {
            count += 1;
            if let Err(e) = check(&file, c) {
                failures.push(format!(
                    "{} {} ({}): {e}",
                    path.file_name().unwrap().to_string_lossy(),
                    c.name,
                    c.source.as_deref().unwrap_or("tanvo")
                ));
            }
        }
    }
    assert!(
        failures.is_empty(),
        "{} of {count} cases failed:\n{}",
        failures.len(),
        failures.join("\n")
    );
}
