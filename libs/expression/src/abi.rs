//! The WASM module's interface.
//!
//! # Calls
//!
//! A host allocates a buffer with `expr_alloc(len) -> ptr`, writes a request
//! into it, calls `expr_parse` or `expr_evaluate` with the buffer's address
//! and length, and frees it with `expr_free(ptr, len)`. A call returns its
//! response as a buffer it allocated, packed in a `u64` with the address in
//! the high 32 bits and the length in the low ones; the host frees it with
//! `expr_free` too. A call returns 0 when the request is malformed.
//!
//! * `expr_parse(ptr, len) -> u64`: the request is the expression's UTF-8
//!   text; the response is a *parse header* alone.
//! * `expr_evaluate(ptr, len) -> u64`: evaluates one expression for a batch
//!   of events. The response is a parse header followed, when the expression
//!   parsed, by one result per event.
//!
//! The module keeps the expressions it parsed in a small cache, so a batch
//! after the first does not parse its expression again. It decodes only the
//! attributes of an event that the expression reads, and checks the others.
//!
//! # Encoding
//!
//! Integers are little-endian. A `str` is a `u32` byte length followed by
//! UTF-8 bytes.
//!
//! ```text
//! evaluate request = str expression, u32 count, event × count
//! event            = str code, value timestamp, u32 count, (str name, value) × count
//! value            = u8 tag (0 string, 1 number), str text
//!
//! parse header     = u8 0                                  the expression parsed
//!                  | u8 1, u8 kind, u32 line, u32 column, u32 offset, str message
//! kind             = 0 syntax error (line, column: 1-based; offset: bytes)
//!                  | 1 wrong number of arguments | 2 invalid decimal literal
//!                  | 3 nested too deeply (the position of the first
//!                    parenthesis beyond the limit)
//!                  | 4 too long
//!                    (line, column and offset are 0 for kinds 1, 2 and 4)
//!
//! evaluate response = parse header, [u32 count, result × count]
//! result            = u8 0, str decimal   a number, as BigDecimal prints it
//!                   | u8 1, str text      a string
//!                   | u8 2, u8 kind, str message
//! error kind        = 0 empty argument list | 1 expected a decimal
//!                   | 2 missing variable | 3 division by zero
//!                   | 4 invalid event (a number value that is not a decimal)
//!                   | 5 rounding digits out of range
//!                   | 6 number out of range
//! ```
//!
//! A number value's text is parsed as a decimal (`12.5`, `-3`, `1e3`); a
//! string value is a number when its text is one, as lago-expression reads
//! properties.
//!
//! # Limits
//!
//! An expression is at most [`MAX_SOURCE_LEN`](crate::MAX_SOURCE_LEN) bytes
//! long and nests parentheses at most [`MAX_DEPTH`](crate::MAX_DEPTH) deep
//! (parse kinds 4 and 3). A number value longer than
//! [`MAX_NUMBER_LEN`](crate::MAX_NUMBER_LEN) bytes makes its event invalid,
//! like one that is not a decimal; numbers within it are bounded as the
//! crate documents (error kinds 5 and 6). With these limits no request makes
//! the module trap, short of one too large for its memory.
//!
//! Every `unsafe` block of the crate is in this module.

use std::{cell::RefCell, collections::HashMap};

use bigdecimal::BigDecimal;

use crate::{
    Event, EventAttribute, Expression, ExpressionError, ExpressionParser, ExpressionValue,
    Function, MAX_NUMBER_LEN, ParseError, PropertyValue, evaluate::parse_number,
};

/// How many parsed expressions the module keeps; the cache is emptied when it
/// is full.
const CACHE_SIZE: usize = 64;

thread_local! {
    static CACHE: RefCell<HashMap<String, Compiled>> = RefCell::new(HashMap::new());
}

/// A parsed expression with the event attributes it reads.
struct Compiled {
    expr: Expression,
    uses: Uses,
}

/// The event attributes an expression reads. An event's other attributes are
/// only checked, not decoded: an event is invalid when any of its numbers is
/// not a decimal, as when lago-api builds it.
#[derive(Default)]
struct Uses {
    code: bool,
    timestamp: bool,
    properties: Vec<String>,
}

impl Uses {
    fn of(expr: &Expression) -> Self {
        let mut uses = Self::default();
        uses.add(expr);
        uses
    }

    fn add(&mut self, expr: &Expression) {
        match expr {
            Expression::EventAttribute(EventAttribute::Code) => self.code = true,
            Expression::EventAttribute(EventAttribute::Timestamp) => self.timestamp = true,
            Expression::EventAttribute(EventAttribute::Properties(name)) => {
                if !self.properties.contains(name) {
                    self.properties.push(name.clone());
                }
            }
            Expression::Function(
                Function::Concat(args) | Function::Least(args) | Function::Greatest(args),
            ) => args.iter().for_each(|e| self.add(e)),
            Expression::Function(
                Function::Ceil(e, digits) | Function::Round(e, digits) | Function::Floor(e, digits),
            ) => {
                self.add(e);
                if let Some(d) = digits {
                    self.add(d);
                }
            }
            Expression::UnaryMinus(e) => self.add(e),
            Expression::BinOp { lhs, rhs, .. } => {
                self.add(lhs);
                self.add(rhs);
            }
            Expression::String(_) | Expression::Decimal(_) => {}
        }
    }

    fn property(&self, name: &str) -> bool {
        self.properties.iter().any(|p| p == name)
    }
}

/// Whether text is a decimal no longer than [`MAX_NUMBER_LEN`], without
/// building it for the common integers.
fn is_decimal(text: &str) -> bool {
    let digits = text.strip_prefix('-').unwrap_or(text);
    text.len() <= MAX_NUMBER_LEN
        && ((!digits.is_empty() && digits.bytes().all(|b| b.is_ascii_digit()))
            || text.parse::<BigDecimal>().is_ok())
}

const TAG_STRING: u8 = 0;
const TAG_NUMBER: u8 = 1;
const RESULT_NUMBER: u8 = 0;
const RESULT_STRING: u8 = 1;
const RESULT_ERROR: u8 = 2;

/// Why an event could not be evaluated, as the response encodes it.
fn error_kind(e: &ExpressionError) -> u8 {
    match e {
        ExpressionError::EmptyArgumentList => 0,
        ExpressionError::ExpectedDecimal => 1,
        ExpressionError::MissingVariable(_) => 2,
        ExpressionError::DivisionByZero => 3,
        ExpressionError::DigitsOutOfRange => 5,
        ExpressionError::NumberOutOfRange => 6,
    }
}

const ERROR_INVALID_EVENT: u8 = 4;
/// lago-api's message when an event's number is not a decimal.
const INVALID_NUMBER: &str = "Failed to parse a number as bigdecimal";

/// Reads a request.
struct Reader<'a>(&'a [u8]);

impl<'a> Reader<'a> {
    fn bytes(&mut self, n: usize) -> Option<&'a [u8]> {
        if self.0.len() < n {
            return None;
        }
        let (head, tail) = self.0.split_at(n);
        self.0 = tail;
        Some(head)
    }

    fn u8(&mut self) -> Option<u8> {
        self.bytes(1).map(|b| b[0])
    }

    fn u32(&mut self) -> Option<u32> {
        self.bytes(4)
            .map(|b| u32::from_le_bytes([b[0], b[1], b[2], b[3]]))
    }

    fn str(&mut self) -> Option<&'a str> {
        let n = self.u32()? as usize;
        std::str::from_utf8(self.bytes(n)?).ok()
    }

    /// A value, or Err(()) for a number that is not a decimal.
    fn value(&mut self) -> Option<Result<PropertyValue, ()>> {
        let tag = self.u8()?;
        let text = self.str()?;
        match tag {
            TAG_STRING => Some(Ok(PropertyValue::String(text.to_owned()))),
            TAG_NUMBER => Some(parse_number(text).map(PropertyValue::Number).ok_or(())),
            _ => None,
        }
    }

    /// Checks a value that is not decoded: Err(()) for a number that is not
    /// a decimal.
    fn skip_value(&mut self) -> Option<Result<(), ()>> {
        let tag = self.u8()?;
        let text = self.str()?;
        match tag {
            TAG_STRING => Some(Ok(())),
            TAG_NUMBER if is_decimal(text) => Some(Ok(())),
            TAG_NUMBER => Some(Err(())),
            _ => None,
        }
    }

    /// An event with the attributes of uses, or Err(()) when one of its
    /// numbers is not a decimal.
    fn event(&mut self, uses: &Uses) -> Option<Result<Event, ()>> {
        let code = self.str()?;
        let mut event = Event::default();
        if uses.code {
            event.code = code.to_owned();
        }
        let mut valid = true;
        if uses.timestamp {
            match self.value()? {
                Ok(v) => event.timestamp = v,
                Err(()) => valid = false,
            }
        } else {
            valid &= self.skip_value()?.is_ok();
        }
        let n = self.u32()? as usize;
        for _ in 0..n {
            let name = self.str()?;
            if uses.property(name) {
                match self.value()? {
                    Ok(v) => {
                        event.properties.insert(name.to_owned(), v);
                    }
                    Err(()) => valid = false,
                }
            } else {
                valid &= self.skip_value()?.is_ok();
            }
        }
        Some(if valid { Ok(event) } else { Err(()) })
    }
}

/// Writes a response.
struct Writer(Vec<u8>);

impl Writer {
    fn u8(&mut self, v: u8) {
        self.0.push(v);
    }

    fn u32(&mut self, v: usize) {
        let v = u32::try_from(v).unwrap_or(u32::MAX);
        self.0.extend_from_slice(&v.to_le_bytes());
    }

    fn str(&mut self, s: &str) {
        self.u32(s.len());
        self.0.extend_from_slice(s.as_bytes());
    }

    fn parse_error(&mut self, e: &ParseError) {
        self.u8(1);
        let (kind, line, column, offset) = match e {
            ParseError::FailedToParse(s) => (0, s.line, s.column, s.offset),
            ParseError::WrongNumberOfArguments(..) => (1, 0, 0, 0),
            ParseError::FailedToParseBigDecimal(_) => (2, 0, 0, 0),
            ParseError::TooDeep(s) => (3, s.line, s.column, s.offset),
            ParseError::TooLong(_) => (4, 0, 0, 0),
        };
        self.u8(kind);
        self.u32(line);
        self.u32(column);
        self.u32(offset);
        self.str(&e.to_string());
    }

    fn result(&mut self, r: Result<ExpressionValue, ExpressionError>) {
        match r {
            Ok(ExpressionValue::Number(d)) => {
                self.u8(RESULT_NUMBER);
                self.str(&d.to_string());
            }
            Ok(ExpressionValue::String(s)) => {
                self.u8(RESULT_STRING);
                self.str(&s);
            }
            Err(e) => {
                self.u8(RESULT_ERROR);
                self.u8(error_kind(&e));
                self.str(&e.to_string());
            }
        }
    }
}

/// Runs f with the parsed expression of src, parsing it at most once while it
/// stays in the cache.
fn with_expression<T>(src: &str, f: impl FnOnce(&Compiled) -> T) -> Result<T, ParseError> {
    CACHE.with_borrow_mut(|cache| {
        if let Some(compiled) = cache.get(src) {
            return Ok(f(compiled));
        }
        let expr = ExpressionParser::parse_expression(src)?;
        let compiled = Compiled {
            uses: Uses::of(&expr),
            expr,
        };
        let out = f(&compiled);
        if cache.len() >= CACHE_SIZE {
            cache.clear();
        }
        cache.insert(src.to_owned(), compiled);
        Ok(out)
    })
}

/// Answers a parse request: the parse header of the expression `src`.
pub fn parse(src: &[u8]) -> Option<Vec<u8>> {
    let src = std::str::from_utf8(src).ok()?;
    let mut w = Writer(Vec::with_capacity(1));
    match with_expression(src, |_| ()) {
        Ok(()) => w.u8(0),
        Err(e) => w.parse_error(&e),
    }
    Some(w.0)
}

/// Answers an evaluate request, or None when it is malformed.
pub fn evaluate(request: &[u8]) -> Option<Vec<u8>> {
    let mut r = Reader(request);
    let src = r.str()?;
    let count = r.u32()? as usize;
    let parsed = with_expression(src, |compiled| {
        // Results are usually short numbers.
        let mut out = Writer(Vec::with_capacity(5 + count.min(request.len()) * 16));
        out.u8(0);
        out.u32(count);
        for _ in 0..count {
            match r.event(&compiled.uses)? {
                Ok(event) => out.result(compiled.expr.evaluate(&event)),
                Err(()) => {
                    out.u8(RESULT_ERROR);
                    out.u8(ERROR_INVALID_EVENT);
                    out.str(INVALID_NUMBER);
                }
            }
        }
        r.0.is_empty().then_some(out.0)
    });
    match parsed {
        Ok(out) => out,
        Err(e) => {
            let mut w = Writer(Vec::new());
            w.parse_error(&e);
            Some(w.0)
        }
    }
}

/// Hands a response to the host: its address in the high 32 bits, its length
/// in the low ones.
fn give(bytes: Vec<u8>) -> u64 {
    let len = bytes.len();
    let ptr = Box::into_raw(bytes.into_boxed_slice()) as *mut u8;
    ((ptr as usize as u64) << 32) | len as u64
}

/// # Safety
/// The buffer must be one `expr_alloc` returned for `len` bytes or more.
unsafe fn read<'a>(ptr: *const u8, len: usize) -> &'a [u8] {
    if len == 0 || ptr.is_null() {
        return &[];
    }
    // SAFETY: the caller passes a buffer of expr_alloc holding len bytes.
    unsafe { std::slice::from_raw_parts(ptr, len) }
}

/// Allocates a buffer of len bytes for a request.
#[unsafe(no_mangle)]
pub extern "C" fn expr_alloc(len: usize) -> *mut u8 {
    Box::into_raw(vec![0u8; len].into_boxed_slice()) as *mut u8
}

/// Frees a buffer of `expr_alloc` or of a response.
///
/// # Safety
/// The buffer must be one this module allocated, with its length.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn expr_free(ptr: *mut u8, len: usize) {
    if ptr.is_null() {
        return;
    }
    // SAFETY: the buffer is a boxed slice of len bytes.
    drop(unsafe { Box::from_raw(std::ptr::slice_from_raw_parts_mut(ptr, len)) });
}

/// Parses the expression in the buffer and returns its parse header.
///
/// # Safety
/// See `read`.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn expr_parse(ptr: *const u8, len: usize) -> u64 {
    // SAFETY: as the caller promises.
    parse(unsafe { read(ptr, len) }).map_or(0, give)
}

/// Answers the evaluate request in the buffer.
///
/// # Safety
/// See `read`.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn expr_evaluate(ptr: *const u8, len: usize) -> u64 {
    // SAFETY: as the caller promises.
    evaluate(unsafe { read(ptr, len) }).map_or(0, give)
}

#[cfg(test)]
mod tests {
    use super::*;

    enum V<'a> {
        S(&'a str),
        N(&'a str),
    }

    fn put_str(b: &mut Vec<u8>, s: &str) {
        b.extend_from_slice(&(s.len() as u32).to_le_bytes());
        b.extend_from_slice(s.as_bytes());
    }

    fn put_value(b: &mut Vec<u8>, v: &V) {
        match v {
            V::S(s) => {
                b.push(TAG_STRING);
                put_str(b, s);
            }
            V::N(s) => {
                b.push(TAG_NUMBER);
                put_str(b, s);
            }
        }
    }

    type TestEvent<'a> = (&'a str, V<'a>, Vec<(&'a str, V<'a>)>);

    fn request(src: &str, events: &[TestEvent]) -> Vec<u8> {
        let mut b = Vec::new();
        put_str(&mut b, src);
        b.extend_from_slice(&(events.len() as u32).to_le_bytes());
        for (code, ts, props) in events {
            put_str(&mut b, code);
            put_value(&mut b, ts);
            b.extend_from_slice(&(props.len() as u32).to_le_bytes());
            for (k, v) in props {
                put_str(&mut b, k);
                put_value(&mut b, v);
            }
        }
        b
    }

    #[derive(Debug, PartialEq)]
    enum Out {
        Number(String),
        String(String),
        Error(u8, String),
    }

    fn decode(resp: &[u8]) -> Result<Vec<Out>, (u8, u32, u32, u32, String)> {
        let mut r = Reader(resp);
        match r.u8().unwrap() {
            0 => {}
            _ => {
                let kind = r.u8().unwrap();
                let (l, c, o) = (r.u32().unwrap(), r.u32().unwrap(), r.u32().unwrap());
                return Err((kind, l, c, o, r.str().unwrap().to_owned()));
            }
        }
        if r.0.is_empty() {
            return Ok(vec![]);
        }
        let n = r.u32().unwrap();
        let out = (0..n)
            .map(|_| match r.u8().unwrap() {
                RESULT_NUMBER => Out::Number(r.str().unwrap().to_owned()),
                RESULT_STRING => Out::String(r.str().unwrap().to_owned()),
                _ => {
                    let kind = r.u8().unwrap();
                    Out::Error(kind, r.str().unwrap().to_owned())
                }
            })
            .collect();
        assert!(r.0.is_empty(), "trailing bytes");
        Ok(out)
    }

    #[test]
    fn test_evaluate_batch() {
        let req = request(
            "event.properties.a * 2",
            &[
                ("c", V::N("1"), vec![("a", V::S("1.5"))]),
                ("c", V::N("1"), vec![("a", V::N("3"))]),
                ("c", V::N("1"), vec![("a", V::S("x"))]),
                ("c", V::N("1"), vec![]),
                ("c", V::N("1"), vec![("a", V::N("NaN"))]),
                // Numbers the expression does not read are checked too.
                ("c", V::N("x"), vec![("a", V::N("1"))]),
                ("c", V::N("1"), vec![("a", V::N("1")), ("b", V::N("1.5.1"))]),
                (
                    "c",
                    V::N("-1"),
                    vec![("a", V::N("-2e1")), ("b", V::N("-0.5"))],
                ),
            ],
        );
        let got = decode(&evaluate(&req).unwrap()).unwrap();
        assert_eq!(
            got,
            vec![
                Out::Number("3.0".into()),
                Out::Number("6".into()),
                Out::Error(1, "Expected a decimal".into()),
                Out::Error(2, "Variable: a not found".into()),
                Out::Error(4, INVALID_NUMBER.into()),
                Out::Error(4, INVALID_NUMBER.into()),
                Out::Error(4, INVALID_NUMBER.into()),
                Out::Number("-40".into()),
            ]
        );
    }

    #[test]
    fn test_evaluate_attributes() {
        let req = request(
            "concat(event.code, '@', event.timestamp)",
            &[("api", V::N("1700000000"), vec![])],
        );
        let got = decode(&evaluate(&req).unwrap()).unwrap();
        assert_eq!(got, vec![Out::String("api@1700000000".into())]);
    }

    #[test]
    fn test_evaluate_cached_expression() {
        for _ in 0..3 {
            let req = request("1 / 0", &[("c", V::N("0"), vec![])]);
            let got = decode(&evaluate(&req).unwrap()).unwrap();
            assert_eq!(got, vec![Out::Error(3, "Division by zero".into())]);
        }
    }

    #[test]
    fn test_evaluate_no_events() {
        let got = decode(&evaluate(&request("1", &[])).unwrap()).unwrap();
        assert_eq!(got, vec![]);
    }

    #[test]
    fn test_evaluate_parse_error() {
        let req = request("1 +", &[("c", V::N("0"), vec![])]);
        let (kind, line, column, offset, message) = decode(&evaluate(&req).unwrap()).unwrap_err();
        assert_eq!((kind, line, column, offset), (0, 1, 4, 3));
        assert!(message.contains("expected"), "got = {message}");
    }

    #[test]
    fn test_parse() {
        assert_eq!(decode(&parse(b"round(1, 2)").unwrap()), Ok(vec![]));
        let (kind, _, _, _, message) = decode(&parse(b"round(1, 2, 3)").unwrap()).unwrap_err();
        assert_eq!(kind, 1);
        assert_eq!(
            message,
            "Wrong number of arguments to function round, expected: 1..2, provided: 3"
        );
        let (kind, line, column, offset, _) = decode(&parse(b"1\n+ +").unwrap()).unwrap_err();
        assert_eq!((kind, line, column, offset), (0, 1, 2, 1));
    }

    #[test]
    fn test_malformed_requests() {
        let req = request("1", &[("c", V::N("0"), vec![])]);
        for n in 0..req.len() {
            assert!(evaluate(&req[..n]).is_none(), "prefix of {n} bytes");
        }
        let mut long = req.clone();
        long.push(0);
        assert!(evaluate(&long).is_none());
        assert!(parse(&[0xff]).is_none());
        let mut bad_tag = request("1", &[("c", V::N("0"), vec![])]);
        let at = 4 + 1 + 4 + 4 + 1;
        bad_tag[at] = 7;
        assert!(evaluate(&bad_tag).is_none());
    }
}
