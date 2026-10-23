//! The parser: `grammar.pest` read into an [`Expression`] tree.
//!
//! Ported from lago-expression `expression-core/src/parser.rs`, with two
//! limits it does not have: an expression is at most [`MAX_SOURCE_LEN`] bytes
//! ([`ParseError::TooLong`]) and nests parentheses, those of function calls
//! included, at most [`MAX_DEPTH`] deep ([`ParseError::TooDeep`]). Both are
//! checked before the grammar runs, whose recursion follows the nesting.

use std::sync::LazyLock;

use bigdecimal::BigDecimal;
use pest::{iterators::Pairs, pratt_parser::PrattParser};

use pest::Parser;
use pest::error::{InputLocation, LineColLocation};
use thiserror::Error;

#[derive(pest_derive::Parser)]
#[grammar = "grammar.pest"]
pub struct ExpressionParser;

/// The longest expression, in bytes.
pub const MAX_SOURCE_LEN: usize = 4096;

/// The deepest nesting of parentheses in an expression.
pub const MAX_DEPTH: usize = 128;

impl ExpressionParser {
    pub fn parse_expression(input: &str) -> ParseResult<Expression> {
        if input.len() > MAX_SOURCE_LEN {
            return Err(ParseError::TooLong(input.len()));
        }
        check_depth(input)?;
        let mut pairs = Self::parse(Rule::root, input)
            .map_err(|e| ParseError::FailedToParse(SyntaxError::new(&e)))?;

        let inner = pairs.next().unwrap().into_inner();
        parse_expr(inner)
    }
}

pub type ParseResult<T> = Result<T, ParseError>;

/// Rejects an input whose parentheses outside string literals nest deeper
/// than [`MAX_DEPTH`], at the first one beyond it.
fn check_depth(input: &str) -> ParseResult<()> {
    let (mut depth, mut in_string) = (0usize, false);
    let (mut line, mut line_start) = (1, 0);
    for (offset, c) in input.char_indices() {
        match c {
            '\'' => in_string = !in_string,
            '\n' => {
                line += 1;
                line_start = offset + 1;
            }
            '(' if !in_string => {
                depth += 1;
                if depth > MAX_DEPTH {
                    let column = input[line_start..offset].chars().count() + 1;
                    return Err(ParseError::TooDeep(SyntaxError {
                        message: format!(
                            "Expression nested too deeply, expected at most {MAX_DEPTH} levels"
                        ),
                        line,
                        column,
                        offset,
                    }));
                }
            }
            ')' if !in_string => depth = depth.saturating_sub(1),
            _ => {}
        }
    }
    Ok(())
}

#[derive(Debug, PartialEq)]
pub enum Function {
    Concat(Vec<Expression>),
    Ceil(Box<Expression>, Option<Box<Expression>>),
    Round(Box<Expression>, Option<Box<Expression>>),
    Floor(Box<Expression>, Option<Box<Expression>>),
    Least(Vec<Expression>),
    Greatest(Vec<Expression>),
}

#[derive(Debug, PartialEq)]
pub enum EventAttribute {
    Code,
    Timestamp,
    Properties(String),
}

#[derive(Debug, PartialEq)]
pub enum Expression {
    EventAttribute(EventAttribute),
    Function(Function),
    String(String),
    Decimal(BigDecimal),
    UnaryMinus(Box<Expression>),
    BinOp {
        lhs: Box<Expression>,
        op: Operation,
        rhs: Box<Expression>,
    },
}

/// An input the grammar does not accept, with where it stops matching.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SyntaxError {
    /// pest's report: the position, the input line and what was expected.
    pub message: String,
    /// The 1-based line of the position.
    pub line: usize,
    /// The 1-based column of the position, in characters.
    pub column: usize,
    /// The byte offset of the position in the input.
    pub offset: usize,
}

impl SyntaxError {
    fn new(e: &pest::error::Error<Rule>) -> Self {
        let (line, column) = match e.line_col {
            LineColLocation::Pos(pos) | LineColLocation::Span(pos, _) => pos,
        };
        let offset = match e.location {
            InputLocation::Pos(pos) | InputLocation::Span((pos, _)) => pos,
        };
        Self {
            message: e.to_string(),
            line,
            column,
            offset,
        }
    }
}

impl std::fmt::Display for SyntaxError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.message)
    }
}

#[derive(Error, Debug)]
pub enum ParseError {
    #[error("{0}")]
    FailedToParse(SyntaxError),

    #[error("Wrong number of arguments to function {0}, expected: {1}, provided: {2}")]
    WrongNumberOfArguments(String, String, usize),

    #[error("bigdecimal parsing error: {0}")]
    FailedToParseBigDecimal(#[from] ::bigdecimal::ParseBigDecimalError),

    /// Parentheses nested deeper than [`MAX_DEPTH`], at the first one beyond.
    #[error("{0}")]
    TooDeep(SyntaxError),

    /// An expression longer than [`MAX_SOURCE_LEN`] bytes, with its length.
    #[error("Expression too long: {0} bytes, expected at most 4096")]
    TooLong(usize),
}

#[derive(Debug, PartialEq)]
pub enum Operation {
    Add,
    Subtract,
    Multiply,
    Divide,
}

fn parse_function(pairs: Pairs<Rule>) -> ParseResult<Function> {
    let mut iter = pairs.into_iter();
    let name = iter.next().unwrap();
    let function = match name.as_rule() {
        Rule::concat => {
            let args = iter
                .map(|r| parse_expr(r.into_inner()))
                .collect::<ParseResult<Vec<Expression>>>()?;

            Function::Concat(args)
        }
        Rule::ceil => parse_function_with_args("ceil", Function::Ceil, iter)?,
        Rule::round => parse_function_with_args("round", Function::Round, iter)?,
        Rule::floor => parse_function_with_args("floor", Function::Floor, iter)?,
        Rule::least => {
            let args = iter
                .map(|r| parse_expr(r.into_inner()))
                .collect::<ParseResult<Vec<Expression>>>()?;

            Function::Least(args)
        }
        Rule::greatest => {
            let args = iter
                .map(|r| parse_expr(r.into_inner()))
                .collect::<ParseResult<Vec<Expression>>>()?;

            Function::Greatest(args)
        }
        rule => unreachable!("Expected function name, got :{:?}", rule),
    };
    Ok(function)
}

fn parse_function_with_args<F>(name: &str, f: F, iter: Pairs<Rule>) -> ParseResult<Function>
where
    F: Fn(Box<Expression>, Option<Box<Expression>>) -> Function,
{
    let mut args = iter
        .map(|r| {
            let expr = parse_expr(r.into_inner())?;
            Ok(Box::new(expr))
        })
        .collect::<Vec<ParseResult<Box<Expression>>>>();

    match args.len() {
        1 => Ok(f(args.remove(0)?, None)),
        2 => Ok(f(args.remove(0)?, Some(args.remove(0)?))),
        // lago-expression names every function "round" here.
        n => Err(ParseError::WrongNumberOfArguments(
            name.to_owned(),
            "1..2".to_owned(),
            n,
        )),
    }
}

fn parse_event_attribute(mut pairs: Pairs<Rule>) -> EventAttribute {
    let mut inner = pairs.next().unwrap().into_inner();
    match inner.next().unwrap().as_rule() {
        Rule::event_code => EventAttribute::Code,
        Rule::event_timestamp => EventAttribute::Timestamp,
        Rule::event_properties => {
            EventAttribute::Properties(inner.next().unwrap().as_str().to_owned())
        }
        rule => unreachable!("expected an event attribute, got: {rule:?}"),
    }
}

static PRATT_PARSER: LazyLock<PrattParser<Rule>> = LazyLock::new(|| {
    use Rule::*;
    use pest::pratt_parser::{Assoc::*, Op};

    // Precedence is defined lowest to highest
    PrattParser::new()
        // Addition and subtract have equal precedence
        .op(Op::infix(add, Left) | Op::infix(subtract, Left))
        .op(Op::infix(multiply, Left) | Op::infix(divide, Left))
        .op(Op::prefix(unary_minus))
});

fn parse_expr(pairs: Pairs<Rule>) -> ParseResult<Expression> {
    PRATT_PARSER
        .map_primary(|primary| {
            let value = match primary.as_rule() {
                Rule::function => Expression::Function(parse_function(primary.into_inner())?),
                Rule::decimal => Expression::Decimal(primary.as_str().parse()?),
                Rule::expr => parse_expr(primary.into_inner())?,
                Rule::variable => {
                    Expression::EventAttribute(parse_event_attribute(primary.into_inner()))
                }
                Rule::string => Expression::String(primary.into_inner().as_str().to_owned()),
                rule => unreachable!("Expr::parse expected atom, found {:?}", rule),
            };
            Ok(value)
        })
        .map_infix(|lhs, op, rhs| {
            let op = match op.as_rule() {
                Rule::add => Operation::Add,
                Rule::subtract => Operation::Subtract,
                Rule::multiply => Operation::Multiply,
                Rule::divide => Operation::Divide,
                rule => unreachable!("Expr::parse expected infix operation, found {:?}", rule),
            };
            Ok(Expression::BinOp {
                lhs: Box::new(lhs?),
                op,
                rhs: Box::new(rhs?),
            })
        })
        .map_prefix(|op, rhs| match op.as_rule() {
            Rule::unary_minus => Ok(Expression::UnaryMinus(Box::new(rhs?))),
            rule => unreachable!("Expr::parse expected operation, found {:?}", rule),
        })
        .parse(pairs)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn parse_and_compare(input: &str, expected_expr: Expression) {
        match ExpressionParser::parse_expression(input) {
            Ok(expr) => {
                assert_eq!(expr, expected_expr)
            }
            Err(e) => panic!("Failed to parse expression: {:?}", e),
        }
    }

    #[test]
    fn test_parse_expression() {
        parse_and_compare("1", Expression::Decimal(1.into()));
    }

    #[test]
    fn test_parse_invalid_decimal() {
        let result = ExpressionParser::parse_expression("1.1.1");
        assert!(
            matches!(result.as_ref().unwrap_err(), ParseError::FailedToParse(_)),
            "Expected FailedToParse error, got different error: {:?}",
            result.unwrap_err()
        );
    }

    #[test]
    fn test_parse_event_attribute() {
        parse_and_compare(
            "event.timestamp",
            Expression::EventAttribute(EventAttribute::Timestamp),
        );
    }

    #[test]
    fn test_parse_event_properties() {
        parse_and_compare(
            "event.properties.blah",
            Expression::EventAttribute(EventAttribute::Properties("blah".to_owned())),
        );
    }

    #[test]
    fn test_parse_string() {
        parse_and_compare("'test'", Expression::String("test".to_owned()));
    }

    #[test]
    fn test_parse_concat() {
        parse_and_compare(
            "concat('a', 'b')",
            Expression::Function(Function::Concat(vec![
                Expression::String("a".to_owned()),
                Expression::String("b".to_owned()),
            ])),
        );
    }

    #[test]
    fn test_parse_concat_uppercase() {
        parse_and_compare(
            "CONCAT('a', 'b')",
            Expression::Function(Function::Concat(vec![
                Expression::String("a".to_owned()),
                Expression::String("b".to_owned()),
            ])),
        );
    }

    #[test]
    fn test_parse_concat_capitalized() {
        parse_and_compare(
            "Concat('a', 'b')",
            Expression::Function(Function::Concat(vec![
                Expression::String("a".to_owned()),
                Expression::String("b".to_owned()),
            ])),
        );
    }

    #[test]
    fn test_parse_ceil() {
        parse_and_compare(
            "ceil(123)",
            Expression::Function(Function::Ceil(
                Box::new(Expression::Decimal(123.into())),
                None,
            )),
        );
    }

    #[test]
    fn test_parse_ceil_one_arg() {
        parse_and_compare(
            "ceil(123, -1)",
            Expression::Function(Function::Ceil(
                Box::new(Expression::Decimal(123.into())),
                Some(Box::new(Expression::UnaryMinus(Box::new(
                    Expression::Decimal(1.into()),
                )))),
            )),
        );
    }

    #[test]
    fn test_parse_round_one_arg() {
        parse_and_compare(
            "round(123, 1)",
            Expression::Function(Function::Round(
                Box::new(Expression::Decimal(123.into())),
                Some(Box::new(Expression::Decimal(1.into()))),
            )),
        );
    }
    #[test]
    fn test_parse_round() {
        parse_and_compare(
            "round(123)",
            Expression::Function(Function::Round(
                Box::new(Expression::Decimal(123.into())),
                None,
            )),
        );
    }

    #[test]
    fn test_parse_floor_one_arg() {
        parse_and_compare(
            "floor(123, 1)",
            Expression::Function(Function::Floor(
                Box::new(Expression::Decimal(123.into())),
                Some(Box::new(Expression::Decimal(1.into()))),
            )),
        );
    }
    #[test]
    fn test_parse_floor() {
        parse_and_compare(
            "floor(123)",
            Expression::Function(Function::Floor(
                Box::new(Expression::Decimal(123.into())),
                None,
            )),
        );
    }

    #[test]
    fn test_least() {
        parse_and_compare(
            "LEAST(1, 2)",
            Expression::Function(Function::Least(vec![
                Expression::Decimal(1.into()),
                Expression::Decimal(2.into()),
            ])),
        );
    }
    #[test]
    fn test_greatest() {
        parse_and_compare(
            "GREATEST(1, 2)",
            Expression::Function(Function::Greatest(vec![
                Expression::Decimal(1.into()),
                Expression::Decimal(2.into()),
            ])),
        );
    }
}
