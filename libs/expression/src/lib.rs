//! Billable metric expressions: the language of lago-expression, which
//! computes a value from an event when it is ingested.
//!
//! An expression is decimal arithmetic (`+ - * /`, unary minus, parentheses)
//! over number literals, `'strings'`, the event's attributes
//! (`event.code`, `event.timestamp`, `event.properties.<name>`) and the
//! functions `concat`, `round`, `ceil`, `floor`, `least` and `greatest`.
//! Numbers are exact [`bigdecimal::BigDecimal`]s; a property whose text is a
//! number is a number.
//!
//! [`ExpressionParser::parse_expression`] parses an expression and
//! [`Expression::evaluate`] evaluates it for an [`Event`]. The [`abi`]
//! module is the interface of the WASM module the Go host (`go/`) and the
//! web UI run.
//!
//! Expressions are user input, so parsing and evaluation are bounded: an
//! expression is at most [`MAX_SOURCE_LEN`] bytes and nests parentheses at
//! most [`MAX_DEPTH`] deep, and numbers stay within [`MAX_DIGITS`] significant
//! digits and a scale within ±[`MAX_SCALE`] (see the evaluate limits in
//! [`ExpressionError`]).

// The parser pest_derive generates without its std feature names `alloc`.
extern crate alloc;

pub use evaluate::{
    EvaluationResult, ExpressionError, ExpressionValue, MAX_DIGITS, MAX_NUMBER_LEN,
    MAX_ROUND_DIGITS, MAX_SCALE,
};
pub use event::{Event, PropertyValue};
pub use parser::{
    EventAttribute, Expression, ExpressionParser, Function, MAX_DEPTH, MAX_SOURCE_LEN, Operation,
    ParseError, SyntaxError,
};

pub mod abi;
mod evaluate;
mod event;
mod parser;
