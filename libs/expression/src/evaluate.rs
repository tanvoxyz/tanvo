//! Evaluation of a parsed expression for an event.
//!
//! Ported from lago-expression `expression-core/src/evaluate.rs`.
//!
//! # Limits
//!
//! Expressions and events are user input, so evaluation bounds the size of
//! every number it handles instead of relying on the host to stop a runaway
//! computation (lago-expression has no such bounds):
//!
//! * every number, from a literal, an event or an operation, has at most
//!   [`MAX_DIGITS`] significant digits and a scale (digits after the decimal
//!   point, negative for trailing zeros) within ±[`MAX_SCALE`]; any other
//!   fails with [`ExpressionError::NumberOutOfRange`]. Each operation's
//!   operands are within these bounds, so no intermediate result grows beyond
//!   a few thousand digits; a division keeps bigdecimal's 100 significant
//!   digits;
//! * the digits argument of `round`, `ceil` and `floor` is within
//!   ±[`MAX_ROUND_DIGITS`], checked before rounding; any other fails with
//!   [`ExpressionError::DigitsOutOfRange`];
//! * a string property longer than [`MAX_NUMBER_LEN`] bytes is a string, never
//!   parsed as a number.

use std::fmt::Display;

use bigdecimal::{BigDecimal, RoundingMode, ToPrimitive, Zero};
use thiserror::Error;

use crate::{
    Event, PropertyValue,
    parser::{EventAttribute, Expression, Function, Operation},
};

/// The most significant digits of a number: enough for [`MAX_SCALE`] digits
/// on either side of the decimal point, so that rounding any number up to
/// 10^1000 to [`MAX_ROUND_DIGITS`] digits stays within it.
pub const MAX_DIGITS: u64 = 2000;

/// The largest scale of a number, either way: a number has at most this many
/// digits after the decimal point, and at most this many trailing zeros
/// before it that are not significant.
pub const MAX_SCALE: i64 = 1000;

/// The largest number of digits, either way, that `round`, `ceil` and `floor`
/// round to.
pub const MAX_ROUND_DIGITS: i64 = 1000;

/// The longest text, in bytes, that is parsed as a number.
pub const MAX_NUMBER_LEN: usize = 4096;

/// d, when it is within [`MAX_DIGITS`] and [`MAX_SCALE`].
pub(crate) fn checked(d: BigDecimal) -> EvaluationResult<BigDecimal> {
    if d.digits() > MAX_DIGITS || d.fractional_digit_count().abs() > MAX_SCALE {
        return Err(ExpressionError::NumberOutOfRange);
    }
    Ok(d)
}

/// The number text is, when it is a decimal no longer than [`MAX_NUMBER_LEN`].
pub(crate) fn parse_number(text: &str) -> Option<BigDecimal> {
    if text.len() > MAX_NUMBER_LEN {
        return None;
    }
    text.parse().ok()
}

/// A property's string: a number when it is one, as lago-expression reads it.
fn string_value(s: &str) -> EvaluationResult<ExpressionValue> {
    match parse_number(s) {
        Some(d) => Ok(ExpressionValue::Number(checked(d)?)),
        None => Ok(ExpressionValue::String(s.to_owned())),
    }
}

#[derive(Debug, PartialEq)]
pub enum ExpressionValue {
    Number(BigDecimal),
    String(String),
}

impl Expression {
    pub fn evaluate(&self, event: &Event) -> EvaluationResult<ExpressionValue> {
        let evaluated_expr = match self {
            Expression::EventAttribute(attr) => attr.evaluate(event)?,
            Expression::Function(f) => f.evaluate(event)?,
            Expression::String(s) => s.clone().into(),
            Expression::Decimal(d) => checked(d.clone())?.into(),
            Expression::UnaryMinus(inner) => {
                ExpressionValue::Number(-(inner.evaluate(event)?.to_decimal()?))
            }
            Expression::BinOp { lhs, op, rhs } => op.evaluate(lhs.as_ref(), rhs.as_ref(), event)?,
        };

        Ok(evaluated_expr)
    }
}

impl ExpressionValue {
    pub fn to_decimal(&self) -> EvaluationResult<BigDecimal> {
        match self {
            ExpressionValue::Number(d) => Ok(d.clone()),
            ExpressionValue::String(_) => Err(ExpressionError::ExpectedDecimal),
        }
    }
}

#[derive(Error, Debug)]
pub enum ExpressionError {
    #[error("Expected non-empty argument list")]
    EmptyArgumentList,

    #[error("Expected a decimal")]
    ExpectedDecimal,

    #[error("Variable: {0} not found")]
    MissingVariable(String),

    #[error("Division by zero")]
    DivisionByZero,

    #[error("Rounding digits out of range, expected: -1000..1000")]
    DigitsOutOfRange,

    #[error(
        "Number out of range, expected: at most 2000 significant digits and a scale within -1000..1000"
    )]
    NumberOutOfRange,
}

pub type EvaluationResult<T> = Result<T, ExpressionError>;

impl From<String> for ExpressionValue {
    fn from(value: String) -> Self {
        ExpressionValue::String(value)
    }
}
impl From<BigDecimal> for ExpressionValue {
    fn from(value: BigDecimal) -> Self {
        ExpressionValue::Number(value)
    }
}

impl From<PropertyValue> for ExpressionValue {
    fn from(value: PropertyValue) -> Self {
        match value {
            PropertyValue::String(s) => match parse_number(&s) {
                Some(d) => d.into(),
                None => s.into(),
            },
            PropertyValue::Number(n) => n.into(),
        }
    }
}

impl Display for ExpressionValue {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            ExpressionValue::Number(d) => d.fmt(f),
            ExpressionValue::String(s) => s.fmt(f),
        }
    }
}

impl Function {
    pub fn evaluate(&self, event: &Event) -> EvaluationResult<ExpressionValue> {
        match self {
            Function::Concat(args) => {
                let evaluated_args = args
                    .iter()
                    .map(|e| e.evaluate(event).map(|v| v.to_string()))
                    .collect::<EvaluationResult<Vec<String>>>()?;

                Ok(ExpressionValue::String(evaluated_args.concat()))
            }
            Function::Round(expr, digit_expr) => evaluate_with_rounding_mode(
                expr.as_ref(),
                digit_expr.as_ref().map(AsRef::as_ref),
                event,
                RoundingMode::HalfUp,
            ),
            Function::Ceil(expr, digit_expr) => evaluate_with_rounding_mode(
                expr.as_ref(),
                digit_expr.as_ref().map(AsRef::as_ref),
                event,
                RoundingMode::Ceiling,
            ),
            Function::Floor(expr, digit_expr) => evaluate_with_rounding_mode(
                expr.as_ref(),
                digit_expr.as_ref().map(AsRef::as_ref),
                event,
                RoundingMode::Floor,
            ),
            Function::Least(args) => {
                let min_value = args
                    .iter()
                    .map(|e| e.evaluate(event).and_then(|v| v.to_decimal()))
                    .collect::<EvaluationResult<Vec<BigDecimal>>>()?
                    .into_iter()
                    .min()
                    .ok_or(ExpressionError::EmptyArgumentList)?;
                Ok(ExpressionValue::Number(min_value))
            }
            Function::Greatest(args) => {
                let max_value = args
                    .iter()
                    .map(|e| e.evaluate(event).and_then(|v| v.to_decimal()))
                    .collect::<EvaluationResult<Vec<BigDecimal>>>()?
                    .into_iter()
                    .max()
                    .ok_or(ExpressionError::EmptyArgumentList)?;
                Ok(ExpressionValue::Number(max_value))
            }
        }
    }
}

fn evaluate_with_rounding_mode(
    expr: &Expression,
    digits: Option<&Expression>,
    event: &Event,
    rounding_mode: RoundingMode,
) -> EvaluationResult<ExpressionValue> {
    let evaluated_decimal = expr.evaluate(event)?.to_decimal()?;
    let round_digits = match digits {
        Some(digit_expr) => {
            let d = digit_expr.evaluate(event)?.to_decimal()?;
            // Checked before rounding, which is linear in the digits.
            if d.abs() > MAX_ROUND_DIGITS {
                return Err(ExpressionError::DigitsOutOfRange);
            }
            d.to_i64().ok_or(ExpressionError::ExpectedDecimal)?
        }
        None => 0,
    };

    Ok(ExpressionValue::Number(checked(
        evaluated_decimal.with_scale_round(round_digits, rounding_mode),
    )?))
}

impl EventAttribute {
    pub fn evaluate(&self, event: &Event) -> EvaluationResult<ExpressionValue> {
        let evaluated_attribute = match self {
            EventAttribute::Code => event.code.to_owned().into(),
            EventAttribute::Timestamp => match &event.timestamp {
                PropertyValue::String(s) => string_value(s)?,
                PropertyValue::Number(d) => checked(d.clone())?.into(),
            },

            EventAttribute::Properties(name) => {
                let value = event
                    .properties
                    .get(name)
                    .ok_or(ExpressionError::MissingVariable(name.clone()))?;

                match value {
                    PropertyValue::String(s) => string_value(s)?,
                    PropertyValue::Number(d) => checked(d.clone())?.into(),
                }
            }
        };
        Ok(evaluated_attribute)
    }
}

impl Operation {
    pub fn evaluate(
        &self,
        lhs: &Expression,
        rhs: &Expression,
        event: &Event,
    ) -> EvaluationResult<ExpressionValue> {
        let lhs_decimal = lhs.evaluate(event)?.to_decimal()?;
        let rhs_decimal = rhs.evaluate(event)?.to_decimal()?;

        let evaluated = match self {
            Operation::Add => lhs_decimal + rhs_decimal,
            Operation::Subtract => lhs_decimal - rhs_decimal,
            Operation::Multiply => lhs_decimal * rhs_decimal,
            // BigDecimal panics when dividing by zero, which takes the host process down with it.
            Operation::Divide if rhs_decimal.is_zero() => {
                return Err(ExpressionError::DivisionByZero);
            }
            Operation::Divide => lhs_decimal / rhs_decimal,
        };

        Ok(checked(evaluated)?.into())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn evaluate_and_compare(expr: Expression, event: &Event, expected_result: ExpressionValue) {
        match expr.evaluate(event) {
            Ok(expr) => {
                assert_eq!(expr, expected_result)
            }
            Err(e) => panic!("Failed to evaluate expression: {:?}", e),
        }
    }

    #[test]
    fn test_evaluate_bigdecimal() {
        let expr = Expression::Decimal(123.into());
        let event = Default::default();
        evaluate_and_compare(expr, &event, ExpressionValue::Number(123.into()));
    }

    #[test]
    fn test_evaluate_event_attribute_code() {
        let expr = Expression::EventAttribute(EventAttribute::Code);
        let event = Event {
            code: "result_code".into(),
            ..Default::default()
        };
        evaluate_and_compare(expr, &event, ExpressionValue::String("result_code".into()));
    }

    #[test]
    fn test_evaluate_event_attribute_timestamp() {
        let expr = Expression::EventAttribute(EventAttribute::Timestamp);
        let event = Event {
            timestamp: 1234.into(),
            ..Default::default()
        };
        evaluate_and_compare(expr, &event, ExpressionValue::Number(1234.into()));
    }

    #[test]
    fn test_evaluate_event_attribute_property_decimal() {
        let expr = Expression::EventAttribute(EventAttribute::Properties("bar".into()));
        let properties = vec![("bar".into(), "123".into())].into_iter().collect();
        let event = Event {
            properties,
            ..Default::default()
        };
        evaluate_and_compare(expr, &event, ExpressionValue::Number(123.into()));
    }

    #[test]
    fn test_evaluate_event_attribute_property_no_decimal() {
        let expr = Expression::EventAttribute(EventAttribute::Properties("bar".into()));
        let properties = vec![("bar".into(), "foo".into())].into_iter().collect();
        let event = Event {
            properties,
            ..Default::default()
        };
        evaluate_and_compare(expr, &event, ExpressionValue::String("foo".into()));
    }

    #[test]
    fn test_evaluate_string() {
        let expr = Expression::String("bar".into());
        let event = Default::default();
        evaluate_and_compare(expr, &event, ExpressionValue::String("bar".into()));
    }

    #[test]
    fn test_evaluate_binop_plus() {
        let expr = Expression::BinOp {
            lhs: Box::new(Expression::Decimal(2.into())),
            op: Operation::Add,
            rhs: Box::new(Expression::Decimal(4.into())),
        };
        let event = Default::default();
        evaluate_and_compare(expr, &event, ExpressionValue::Number(6.into()));
    }

    #[test]
    fn test_evaluate_binop_minus() {
        let expr = Expression::BinOp {
            lhs: Box::new(Expression::Decimal(2.into())),
            op: Operation::Subtract,
            rhs: Box::new(Expression::Decimal(4.into())),
        };
        let event = Default::default();
        evaluate_and_compare(expr, &event, ExpressionValue::Number((-2).into()));
    }

    #[test]
    fn test_evaluate_binop_multiply() {
        let expr = Expression::BinOp {
            lhs: Box::new(Expression::Decimal(2.into())),
            op: Operation::Multiply,
            rhs: Box::new(Expression::Decimal(4.into())),
        };
        let event = Default::default();
        evaluate_and_compare(expr, &event, ExpressionValue::Number(8.into()));
    }

    #[test]
    fn test_evaluate_binop_divide() {
        let expr = Expression::BinOp {
            lhs: Box::new(Expression::Decimal(4.into())),
            op: Operation::Divide,
            rhs: Box::new(Expression::Decimal(2.into())),
        };
        let event = Default::default();
        evaluate_and_compare(expr, &event, ExpressionValue::Number(2.into()));
    }

    #[test]
    fn test_evaluate_binop_divide_by_zero() {
        let expr = Expression::BinOp {
            lhs: Box::new(Expression::Decimal(1.into())),
            op: Operation::Divide,
            rhs: Box::new(Expression::Decimal(0.into())),
        };
        let event = Default::default();

        assert!(matches!(
            expr.evaluate(&event),
            Err(ExpressionError::DivisionByZero)
        ));
    }

    #[test]
    fn test_evaluate_unary_minus() {
        let expr = Expression::UnaryMinus(Box::new(Expression::Decimal(12.into())));
        let event = Default::default();
        evaluate_and_compare(expr, &event, ExpressionValue::Number((-12).into()));
    }

    #[test]
    fn test_evaluate_round() {
        let expr = Expression::Function(Function::Round(
            Box::new(Expression::Decimal("12.5".parse::<BigDecimal>().unwrap())),
            None,
        ));
        let event = Default::default();
        evaluate_and_compare(expr, &event, ExpressionValue::Number(13.into()));
    }

    #[test]
    fn test_evaluate_round_two_args() {
        let expr = Expression::Function(Function::Round(
            Box::new(Expression::Decimal("12.345".parse::<BigDecimal>().unwrap())),
            Some(Box::new(Expression::Decimal(2.into()))),
        ));
        let event = Default::default();
        evaluate_and_compare(
            expr,
            &event,
            ExpressionValue::Number("12.35".parse::<BigDecimal>().unwrap()),
        );
    }

    #[test]
    fn test_evaluate_ceil() {
        let expr = Expression::Function(Function::Ceil(
            Box::new(Expression::Decimal("12.3".parse::<BigDecimal>().unwrap())),
            None,
        ));
        let event = Default::default();
        evaluate_and_compare(expr, &event, ExpressionValue::Number(13.into()));
    }

    #[test]
    fn test_evaluate_ceil_with_arg() {
        let expr = Expression::Function(Function::Ceil(
            Box::new(Expression::Decimal("12.351".parse::<BigDecimal>().unwrap())),
            Some(Box::new(Expression::Decimal(1.into()))),
        ));
        let event = Default::default();
        evaluate_and_compare(
            expr,
            &event,
            ExpressionValue::Number("12.4".parse::<BigDecimal>().unwrap()),
        );
    }

    #[test]
    fn test_evaluate_floor() {
        let expr = Expression::Function(Function::Floor(
            Box::new(Expression::Decimal("12.3".parse::<BigDecimal>().unwrap())),
            None,
        ));
        let event = Default::default();
        evaluate_and_compare(expr, &event, ExpressionValue::Number(12.into()));
    }

    #[test]
    fn test_evaluate_floor_with_arg() {
        let expr = Expression::Function(Function::Floor(
            Box::new(Expression::Decimal("12.351".parse::<BigDecimal>().unwrap())),
            Some(Box::new(Expression::Decimal(1.into()))),
        ));
        let event = Default::default();
        evaluate_and_compare(
            expr,
            &event,
            ExpressionValue::Number("12.3".parse::<BigDecimal>().unwrap()),
        );
    }

    #[test]
    fn test_evaluate_concat() {
        let expr = Expression::Function(Function::Concat(vec![
            Expression::String("test".into()),
            Expression::String("-".into()),
            Expression::String("123".into()),
        ]));
        let event = Default::default();
        evaluate_and_compare(expr, &event, ExpressionValue::String("test-123".into()));
    }

    #[test]
    fn test_evaluate_nested_functions() {
        let expr = Expression::Function(Function::Concat(vec![
            Expression::String("test".into()),
            Expression::String("-".into()),
            Expression::Function(Function::Round(
                Box::new(Expression::Decimal("123".parse::<BigDecimal>().unwrap())),
                None,
            )),
        ]));
        let event = Default::default();
        evaluate_and_compare(expr, &event, ExpressionValue::String("test-123".into()));
    }
}
