//! The browser build of the billable metric expressions: the evaluator the
//! server runs (`tanvo-expression`), bound to JavaScript with wasm-bindgen
//! for the previews of the web UI. Its interface is lago-expression's
//! `expression-js`:
//!
//! ```js
//! import init, { parseExpression, evaluateExpression } from 'tanvo-expression';
//!
//! await init();
//! const expression = parseExpression('round(event.properties.size * 1.2, 2)');
//! evaluateExpression(expression, 'storage', 1700000000n, { size: '10.5' }); // "12.6"
//! ```
//!
//! `parseExpression` throws an `Error` with the parser's message when the
//! expression is invalid; `evaluateExpression` throws one when it cannot be
//! evaluated for the event. The result is the value's text: a number as an
//! exact decimal, or a string.

use std::collections::HashMap;

use js_sys::{Object, Reflect};
use tanvo_expression::{Event, Expression, ExpressionParser, PropertyValue};
use wasm_bindgen::prelude::*;

/// A parsed expression, evaluated by [`evaluate_expression`].
#[wasm_bindgen]
pub struct ParsedExpression(Expression);

/// Parses an expression; throws an `Error` with the parser's message when
/// it is invalid.
#[wasm_bindgen(js_name = parseExpression)]
pub fn parse_expression(expression: &str) -> Result<ParsedExpression, JsError> {
    ExpressionParser::parse_expression(expression)
        .map(ParsedExpression)
        .map_err(|e| JsError::new(&e.to_string()))
}

/// Evaluates `expression` for the event of `code`, `timestamp` (Unix
/// seconds, a `bigint`) and `properties`, an object of strings or numbers
/// (a string whose text is a number is a number); returns the value's text.
/// Throws an `Error` when the expression cannot be evaluated for the event.
#[wasm_bindgen(js_name = evaluateExpression)]
pub fn evaluate_expression(
    expression: &ParsedExpression,
    code: String,
    timestamp: u64,
    properties: &JsValue,
) -> Result<String, JsError> {
    let event = Event {
        code,
        timestamp: timestamp.into(),
        properties: event_properties(properties)?,
    };
    expression
        .0
        .evaluate(&event)
        .map(|v| v.to_string())
        .map_err(|e| JsError::new(&e.to_string()))
}

/// Reads the event properties: every own enumerable property of an object
/// whose value is a string, a finite number or a bigint. A number is read
/// from its shortest JavaScript text (`2.3` is 2.3).
fn event_properties(properties: &JsValue) -> Result<HashMap<String, PropertyValue>, JsError> {
    let mut out = HashMap::new();
    if properties.is_undefined() || properties.is_null() {
        return Ok(out);
    }
    if !properties.is_object() {
        return Err(JsError::new("properties: expected an object"));
    }
    let keys = Object::keys(properties.unchecked_ref::<Object>());
    for key in keys.iter() {
        let name = key.as_string().unwrap_or_default();
        let value =
            Reflect::get(properties, &key).map_err(|_| JsError::new("properties: unreadable"))?;
        let text = if let Some(s) = value.as_string() {
            s
        } else if let Some(n) = value.as_f64().filter(|n| n.is_finite()) {
            radix10(js_sys::Number::from(n).to_string_with_radix(10))?
        } else if value.is_bigint() {
            radix10(value.unchecked_ref::<js_sys::BigInt>().to_string(10))?
        } else {
            return Err(JsError::new(&format!(
                "properties.{name}: expected a string or a number"
            )));
        };
        out.insert(name, PropertyValue::String(text));
    }
    Ok(out)
}

/// The text of a number's `toString(10)`.
fn radix10<E>(text: Result<js_sys::JsString, E>) -> Result<String, JsError> {
    text.map(String::from)
        .map_err(|_| JsError::new("properties: unprintable number"))
}
