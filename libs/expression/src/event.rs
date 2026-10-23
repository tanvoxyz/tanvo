//! The event an expression is evaluated for.
//!
//! Ported from lago-expression `expression-core/src/event.rs`.

use std::collections::HashMap;

use bigdecimal::{BigDecimal, FromPrimitive};
use serde::Deserialize;

#[derive(Debug, Default, PartialEq, Deserialize)]
pub struct Event {
    pub code: String,
    pub timestamp: PropertyValue,
    pub properties: HashMap<String, PropertyValue>,
}

#[derive(Debug, PartialEq, Clone, Deserialize)]
#[serde(untagged)]
pub enum PropertyValue {
    String(String),
    Number(BigDecimal),
}

impl Default for PropertyValue {
    fn default() -> Self {
        PropertyValue::Number(0.into())
    }
}

impl From<&str> for PropertyValue {
    fn from(value: &str) -> Self {
        PropertyValue::String(value.into())
    }
}
impl From<String> for PropertyValue {
    fn from(value: String) -> Self {
        PropertyValue::String(value)
    }
}

impl From<u64> for PropertyValue {
    fn from(value: u64) -> Self {
        PropertyValue::Number(value.into())
    }
}

impl From<f64> for PropertyValue {
    fn from(value: f64) -> Self {
        let number = BigDecimal::from_f64(value).expect("valid floating point timestamp given");
        PropertyValue::Number(number)
    }
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    #[test]
    fn test_deserialize_with_numbers() {
        let json = json!({
            "code": "testing",
            "timestamp": 123124123123i64,
            "properties": {
                "text": "text_value",
                "number": 300
            }
        });

        let event = serde_json::from_value::<Event>(json).expect("expected json to parse");

        let code = "testing".to_owned();
        let timestamp = 123124123123.into();
        let mut properties = HashMap::default();
        properties.insert("number".to_owned(), PropertyValue::Number(300.into()));
        properties.insert(
            "text".to_owned(),
            PropertyValue::String("text_value".into()),
        );

        assert_eq!(
            event,
            Event {
                code,
                timestamp,
                properties
            }
        )
    }

    #[test]
    fn test_deserialize_with_timestamp_float() {
        let json = json!({
            "code": "testing",
            "timestamp": 123.4123,
            "properties": {
                "text": "text_value",
                "number": 300
            }
        });

        let event = serde_json::from_value::<Event>(json).expect("expected json to parse");

        let code = "testing".to_owned();
        let timestamp = (123.4123).into();
        let mut properties = HashMap::default();
        properties.insert("number".to_owned(), PropertyValue::Number(300.into()));
        properties.insert(
            "text".to_owned(),
            PropertyValue::String("text_value".into()),
        );

        assert_eq!(
            event,
            Event {
                code,
                timestamp,
                properties
            }
        )
    }

    #[test]
    fn test_deserialize_timestamp_formats() {
        let json = json!({
            "code": "testing",
            "timestamp": "123124123123",
            "properties": {
                "text": "text_value",
                "number": 300
            }
        });

        let event = serde_json::from_value::<Event>(json).expect("expected json to parse");

        let code = "testing".to_owned();
        let timestamp = "123124123123".into();
        let mut properties = HashMap::default();
        properties.insert("number".to_owned(), PropertyValue::Number(300.into()));
        properties.insert(
            "text".to_owned(),
            PropertyValue::String("text_value".into()),
        );

        assert_eq!(
            event,
            Event {
                code,
                timestamp,
                properties
            }
        )
    }

    #[test]
    fn test_deserialize_with_floating_point() {
        let json = r#"{
            "code": "testing",
            "timestamp": 123124123123,
            "properties": {
                "text": "text_value",
                "number": 3.2
            }
        }"#;

        let event = serde_json::from_str::<Event>(json).expect("expected json to parse");

        let code = "testing".to_owned();
        let timestamp = 123124123123.into();
        let mut properties = HashMap::default();
        properties.insert(
            "number".to_owned(),
            PropertyValue::Number("3.2".parse().unwrap()),
        );
        properties.insert(
            "text".to_owned(),
            PropertyValue::String("text_value".into()),
        );

        assert_eq!(
            event,
            Event {
                code,
                timestamp,
                properties
            }
        )
    }
}
