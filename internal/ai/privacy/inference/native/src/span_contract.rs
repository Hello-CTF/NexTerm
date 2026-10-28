//! Opt-in `existing-ip-and-dynamic-url-v1` span contract for AstrLink Guard
//! models. It is syntax-only: it narrows existing `ip_address` spans to the
//! parsed IP literal and splits existing dynamic-host URL spans
//! (`scheme://user:password@%s[:port][/]`) into their `account` and
//! `common_secret` parts. It never creates a span without a model span.
//!
//! Ported from AstrLink Guard `tools/span-worker/refine.rs` (sha256
//! fd7f5cfb3f5198e7a08d82828294fa8c25001fae5091a1dce71283be34c9cd64), which
//! mirrors `astrlink_guard/ip_boundaries.py` and
//! `astrlink_guard/code_span_contract.py`. Offsets are UTF-8 bytes throughout.
//! Scores and text ids are inherited from the model span; a split URL's score
//! is not a calibrated probability of its new account/secret label.

use std::{
    net::{IpAddr, Ipv4Addr},
    sync::OnceLock,
};

use regex::Regex;
use serde_json::Value;

use crate::protocol::DetectedSpan;

// Generated from the frozen Python contracts; kept byte-identical to the
// generator output.
#[rustfmt::skip]
mod patterns;
#[rustfmt::skip]
mod unicode;

pub const CONFIG_FIELD: &str = "astrlink_guard_span_contract";
pub const CONTRACT: &str = "existing-ip-and-dynamic-url-v1";

/// Absent or null keeps model spans unchanged; any other value than the known
/// contract fails model loading.
pub fn configured(config: &[u8]) -> Result<bool, &'static str> {
    let config: Value = serde_json::from_slice(config).map_err(|_| "invalid_model_config")?;
    match config.get(CONFIG_FIELD) {
        None | Some(Value::Null) => Ok(false),
        Some(Value::String(value)) if value == CONTRACT => Ok(true),
        _ => Err("invalid_span_contract"),
    }
}

pub fn refine(text: &str, spans: Vec<DetectedSpan>) -> Result<Vec<DetectedSpan>, &'static str> {
    for span in &spans {
        if span.start >= span.end
            || span.end > text.len()
            || !text.is_char_boundary(span.start)
            || !text.is_char_boundary(span.end)
        {
            return Err("invalid_span_offsets");
        }
    }
    Ok(refine_urls(text, &refine_ips(text, &spans)))
}

struct Patterns {
    atom: Regex,
    identifier: Regex,
    scope: Regex,
    port: Regex,
    url: Regex,
    version: Regex,
    word: Regex,
    left: Regex,
    right: Regex,
    atom_char: Regex,
    scheme: Regex,
    slot: Regex,
    url_port: Regex,
    placeholder: Regex,
    literal_forbidden: Regex,
    url_forbidden: Regex,
    url_left: Regex,
}

fn patterns() -> &'static Patterns {
    static VALUE: OnceLock<Patterns> = OnceLock::new();
    VALUE.get_or_init(|| {
        let compile = |source: &str| Regex::new(source).expect("frozen span syntax");
        Patterns {
            atom: compile(patterns::ATOM),
            identifier: compile(patterns::IDENTIFIER),
            scope: compile(patterns::SCOPE),
            port: compile(patterns::PORT),
            url: compile(patterns::URL),
            version: compile(patterns::VERSION),
            word: compile(patterns::WORD),
            left: compile(patterns::LEFT),
            right: compile(patterns::RIGHT),
            atom_char: compile(r"[A-Za-z0-9_.:%~+\-]"),
            scheme: compile(patterns::SCHEME),
            slot: compile(patterns::SLOT),
            url_port: compile(patterns::URL_PORT),
            placeholder: compile(patterns::PLACEHOLDER),
            literal_forbidden: compile(patterns::LITERAL_FORBIDDEN),
            url_forbidden: compile(patterns::URL_FORBIDDEN),
            url_left: compile(r"[A-Za-z0-9_+./:@%\-]"),
        }
    })
}

fn matches_char(pattern: &Regex, character: char) -> bool {
    pattern.is_match(character.encode_utf8(&mut [0; 4]))
}

fn overlap(start: usize, end: usize, other_start: usize, other_end: usize) -> bool {
    start.max(other_start) < end.min(other_end)
}

/// Sorted `(start, end, id)` intervals with running maximum ends, for
/// overlap queries.
struct Index {
    rows: Vec<(usize, usize, usize)>,
    max_ends: Vec<usize>,
}

impl Index {
    fn new(ranges: impl Iterator<Item = (usize, usize, usize)>) -> Self {
        let mut rows = ranges.collect::<Vec<_>>();
        rows.sort_unstable();
        let mut maximum = 0;
        let max_ends = rows
            .iter()
            .map(|(_, end, _)| {
                maximum = maximum.max(*end);
                maximum
            })
            .collect();
        Self { rows, max_ends }
    }

    fn spans(spans: &[DetectedSpan]) -> Self {
        Self::new(
            spans
                .iter()
                .enumerate()
                .map(|(index, span)| (span.start, span.end, index)),
        )
    }

    fn overlapping(&self, start: usize, end: usize) -> impl Iterator<Item = usize> + '_ {
        let first = self.max_ends.partition_point(|&maximum| maximum <= start);
        let stop = self
            .rows
            .partition_point(|&(row_start, _, _)| row_start < end);
        self.rows[first.min(stop)..stop]
            .iter()
            .filter_map(move |&(row_start, row_end, id)| {
                overlap(start, end, row_start, row_end).then_some(id)
            })
    }
}

fn valid_ip(value: &str) -> bool {
    let address = if let Some((address, scope)) = value.split_once('%') {
        if !address.contains(':') || !patterns().scope.is_match(scope) {
            return false;
        }
        address
    } else {
        value
    };
    address.parse::<IpAddr>().is_ok()
}

/// Returns the byte range of the IP literal inside a candidate atom, after an
/// optional `field:` prefix, a sentence period, an IPv4 port or a trailing
/// colon.
fn parse_atom(atom: &str) -> Option<(usize, usize)> {
    if atom.chars().count() > 256 {
        return None;
    }
    let mut start = 0;
    if let Some((field, _)) = atom.split_once(':')
        && patterns().identifier.is_match(field)
        && field
            .chars()
            .any(|character| !character.is_ascii_hexdigit())
    {
        start = field.len() + 1;
    }
    let mut value = &atom[start..];
    if value.is_empty() {
        return None;
    }
    if valid_ip(value) {
        return Some((start, atom.len()));
    }
    let mut end = atom.len();
    if value.ends_with('.') && !value.ends_with("..") {
        value = &value[..value.len() - 1];
        end -= 1;
        if valid_ip(value) {
            return Some((start, end));
        }
    }
    if let Some((host, port)) = value.split_once(':')
        && (port.is_empty() || patterns().port.is_match(port))
        && host.parse::<Ipv4Addr>().is_ok()
    {
        return Some((start, start + host.len()));
    }
    if value.ends_with(':') && !value.ends_with("::") && valid_ip(&value[..value.len() - 1]) {
        return Some((start, end - 1));
    }
    None
}

/// Whether a version field appears within the 80 characters before `end`.
fn version_before(text: &str, end: usize) -> bool {
    let start = text[..end]
        .char_indices()
        .rev()
        .nth(79)
        .map(|(index, _)| index)
        .unwrap_or(0);
    let prefix = &text[start..end];
    patterns().version.find_iter(prefix).any(|found| {
        found.as_str().starts_with('版')
            || found.start() == 0
            || prefix[..found.start()]
                .chars()
                .next_back()
                .is_some_and(|character| !matches_char(&patterns().word, character))
    })
}

fn refine_ips(text: &str, original: &[DetectedSpan]) -> Vec<DetectedSpan> {
    if !original.iter().any(|span| span.label == "ip_address") {
        return original.to_vec();
    }
    let input_index = Index::spans(original);
    let ip_index = Index::new(
        original
            .iter()
            .enumerate()
            .filter(|(_, span)| span.label == "ip_address")
            .map(|(index, span)| (span.start, span.end, index)),
    );
    let url_index = Index::new(
        patterns()
            .url
            .find_iter(text)
            .filter(|found| {
                !found.as_str().starts_with("//")
                    || found.start() == 0
                    || text.as_bytes()[found.start() - 1] != b':'
            })
            .enumerate()
            .map(|(index, found)| (found.start(), found.end(), index)),
    );
    let mut candidates = Vec::new();
    for found in patterns().atom.find_iter(text) {
        if ip_index
            .overlapping(found.start(), found.end())
            .next()
            .is_none()
        {
            continue;
        }
        let Some((relative_start, relative_end)) = parse_atom(found.as_str()) else {
            continue;
        };
        let (start, end) = (found.start() + relative_start, found.start() + relative_end);
        let following = text[found.end()..].chars().next();
        if found.as_str().ends_with('}')
            && following.is_some_and(|character| matches_char(&patterns().atom_char, character))
        {
            continue;
        }
        if text[start..end].contains('%')
            && following.is_some_and(|character| "/@\\$%+".contains(character))
        {
            continue;
        }
        if url_index.overlapping(start, end).next().is_some() || version_before(text, start) {
            continue;
        }
        candidates.push((start, end));
    }
    let candidate_index = Index::new(
        candidates
            .iter()
            .enumerate()
            .map(|(index, &(start, end))| (start, end, index)),
    );
    let mut proposed = original.to_vec();
    for (index, span) in original.iter().enumerate() {
        if span.label != "ip_address" || text[span.start..span.end].chars().count() > 256 {
            continue;
        }
        let mut hits = candidate_index.overlapping(span.start, span.end);
        let Some(candidate) = hits.next() else {
            continue;
        };
        if hits.next().is_some() {
            continue;
        }
        let (start, end) = candidates[candidate];
        let left = if span.start < start {
            &text[span.start..start]
        } else {
            ""
        };
        let right = if span.end > end {
            &text[end..span.end]
        } else {
            ""
        };
        if !patterns().left.is_match(left) || !patterns().right.is_match(right) {
            continue;
        }
        if input_index
            .overlapping(start, end)
            .any(|other| other != index)
        {
            continue;
        }
        proposed[index].start = start;
        proposed[index].end = end;
    }
    let proposed_index = Index::spans(&proposed);
    let conflicts = proposed
        .iter()
        .enumerate()
        .filter_map(|(index, span)| {
            (span != &original[index]
                && proposed_index
                    .overlapping(span.start, span.end)
                    .any(|other| other != index))
            .then_some(index)
        })
        .collect::<Vec<_>>();
    for index in conflicts {
        proposed[index] = original[index].clone();
    }
    proposed
}

fn literal(value: &str) -> bool {
    !value.is_empty()
        && !patterns().slot.is_match(value)
        && !patterns().placeholder.is_match(value)
        && !patterns().literal_forbidden.is_match(value)
        && !value.chars().any(category_c)
}

/// Unicode 14 general category C (Cc, Cf, Cs, Co, Cn), matching Python 3.11.
fn category_c(character: char) -> bool {
    let point = character as u32;
    let index = unicode::CATEGORY_C.partition_point(|&(start, _)| start <= point);
    index > 0 && point <= unicode::CATEGORY_C[index - 1].1
}

/// Splits `scheme://user:password@%s[:port][/]` into the byte ranges of its
/// user and password.
fn url_parts(value: &str) -> Option<[(usize, usize, &'static str); 2]> {
    let scheme = patterns().scheme.find(value)?;
    if scheme.start() != 0 || patterns().url_forbidden.is_match(value) {
        return None;
    }
    let end = value[scheme.end()..]
        .find(['/', '?', '#'])
        .map(|index| index + scheme.end())
        .unwrap_or(value.len());
    let authority = &value[scheme.end()..end];
    if authority.matches('@').count() != 1 {
        return None;
    }
    let (userinfo, host_port) = authority.split_once('@')?;
    if userinfo.matches(':').count() != 1 {
        return None;
    }
    let (host, port) = match host_port.split_once(':') {
        Some((host, port)) => (host, Some(port)),
        None => (host_port, None),
    };
    if !patterns().slot.is_match(host)
        || port.is_some_and(|port| !patterns().url_port.is_match(port))
    {
        return None;
    }
    let (user, password) = userinfo.split_once(':')?;
    if !literal(user) || !literal(password) || !matches!(&value[end..], "" | "/") {
        return None;
    }
    let start = scheme.end();
    Some([
        (start, start + user.len(), "account"),
        (
            start + user.len() + 1,
            start + userinfo.len(),
            "common_secret",
        ),
    ])
}

fn refine_urls(text: &str, original: &[DetectedSpan]) -> Vec<DetectedSpan> {
    let index = Index::spans(original);
    let mut result = Vec::new();
    for (position, span) in original.iter().enumerate() {
        let pieces = if span.label == "url" {
            url_parts(&text[span.start..span.end])
        } else {
            None
        };
        let safe_left = text[..span.start]
            .chars()
            .next_back()
            .is_none_or(|character| !matches_char(&patterns().url_left, character));
        let safe_right = text[span.end..]
            .chars()
            .next()
            .is_none_or(|character| "\n\r\t \"'`),;]}".contains(character));
        let isolated = !index
            .overlapping(span.start, span.end)
            .any(|other| other != position);
        if let Some(pieces) = pieces.filter(|_| safe_left && safe_right && isolated) {
            for (start, end, label) in pieces {
                result.push(DetectedSpan {
                    start: span.start + start,
                    end: span.start + end,
                    label: label.into(),
                    ..span.clone()
                });
            }
        } else {
            result.push(span.clone());
        }
    }
    result
}

#[cfg(test)]
mod tests {
    use super::*;

    fn span(start: usize, end: usize, label: &str) -> DetectedSpan {
        DetectedSpan {
            text_id: 7,
            start,
            end,
            label: label.into(),
            score: 0.8125,
        }
    }

    fn ip(text: &str, needle: &str) -> DetectedSpan {
        let start = text.find(needle).expect("needle");
        span(start, start + needle.len(), "ip_address")
    }

    fn refined_texts(text: &str, spans: Vec<DetectedSpan>) -> Vec<(&str, String)> {
        refine(text, spans)
            .expect("refine")
            .into_iter()
            .map(|span| (&text[span.start..span.end], span.label))
            .collect()
    }

    #[test]
    fn configuration_is_optional_and_strict() {
        assert_eq!(configured(br#"{}"#), Ok(false));
        assert_eq!(
            configured(br#"{"astrlink_guard_span_contract":null}"#),
            Ok(false)
        );
        assert_eq!(
            configured(br#"{"astrlink_guard_span_contract":"existing-ip-and-dynamic-url-v1"}"#),
            Ok(true)
        );
        for invalid in [
            br#"{"astrlink_guard_span_contract":"unknown"}"#.as_slice(),
            br#"{"astrlink_guard_span_contract":"existing-ip-and-dynamic-url-v2"}"#,
            br#"{"astrlink_guard_span_contract":1}"#,
            br#"{"astrlink_guard_span_contract":{}}"#,
        ] {
            assert_eq!(configured(invalid), Err("invalid_span_contract"));
        }
        assert_eq!(configured(b"[1"), Err("invalid_model_config"));
    }

    #[test]
    fn byte_offsets_and_source_scores_survive() {
        let text = "😀甲 IP=203.0.113.27:8443";
        let start = text.find("0.113").unwrap();
        let output = refine(text, vec![span(start, start + 5, "ip_address")]).unwrap();
        assert_eq!(&text[output[0].start..output[0].end], "203.0.113.27");
        assert_eq!(output[0].score, 0.8125);
        assert_eq!(output[0].text_id, 7);
    }

    #[test]
    fn spans_off_utf8_boundaries_fail_closed() {
        let text = "😀甲 IP=203.0.113.27";
        for invalid in [
            span(1, 2, "url"),
            span(0, 5, "ip_address"),
            span(4, 4, "ip_address"),
            span(4, text.len() + 1, "ip_address"),
        ] {
            assert_eq!(refine(text, vec![invalid]), Err("invalid_span_offsets"));
        }
        assert_eq!(refine(text, Vec::new()), Ok(Vec::new()));
    }

    #[test]
    fn ip_spans_narrow_to_the_literal_and_drop_ports_and_punctuation() {
        for (text, needle, expected) in [
            (
                "peer (203.0.113.209), ok",
                "(203.0.113.209),",
                "203.0.113.209",
            ),
            // A partial span grows to the literal after an identifier field.
            ("peer ip:203.0.113.4 ok", "203.0.113", "203.0.113.4"),
            (
                "connect 198.51.100.7:8080 now",
                "198.51.100.7:8080",
                "198.51.100.7",
            ),
            ("host 192.0.2.1.", "192.0.2.1.", "192.0.2.1"),
            ("host [2001:db8::1]:443 up", "[2001:db8::1]", "2001:db8::1"),
            ("gw fe80::1%eth0 up", "fe80::1%eth0", "fe80::1%eth0"),
            ("addr: 2001:db8::5 here", "2001:db8::5", "2001:db8::5"),
        ] {
            assert_eq!(
                refined_texts(text, vec![ip(text, needle)]),
                vec![(expected, "ip_address".to_owned())],
                "{text}"
            );
        }
    }

    #[test]
    fn ip_spans_that_are_not_ips_or_are_ambiguous_are_unchanged() {
        for (text, needle) in [
            // Only whitespace, quotes, brackets and `:` are trimmed on the left.
            ("peer=203.0.113.209 ok", "=203.0.113.209"),
            // An IPv4 address cannot carry a zone id.
            ("gw 192.0.2.1%eth0 up", "192.0.2.1%eth0"),
            // Versions and URLs are not refined.
            ("version 1.2.3.4 installed", "1.2.3.4"),
            ("see http://203.0.113.9/x now", "203.0.113.9"),
            ("value 999.1.1.1 end", "999.1.1.1"),
        ] {
            let original = ip(text, needle);
            assert_eq!(
                refine(text, vec![original.clone()]).unwrap(),
                vec![original],
                "{text}"
            );
        }
        // One span over two IP literals is ambiguous.
        let text = "a 203.0.113.1,203.0.113.2 b";
        let original = ip(text, "203.0.113.1,203.0.113.2");
        assert_eq!(
            refine(text, vec![original.clone()]).unwrap(),
            vec![original]
        );
    }

    #[test]
    fn ip_refinement_never_moves_into_another_span() {
        let text = "x 203.0.113.5:9 y";
        let start = text.find("203").unwrap();
        let spans = vec![
            span(start, start + 11, "ip_address"),
            span(start + 11, start + 13, "account"),
        ];
        let output = refine(text, spans.clone()).unwrap();
        assert_eq!(output[1], spans[1]);
        assert_eq!(&text[output[0].start..output[0].end], "203.0.113.5");
    }

    #[test]
    fn url_splitting_retains_every_credential_byte_and_score() {
        let text = "nats://用户名:密😀码@%s:%d";
        let output = refine(text, vec![span(0, text.len(), "url")]).unwrap();
        assert_eq!(output.len(), 2);
        assert_eq!(&text[output[0].start..output[0].end], "用户名");
        assert_eq!(output[0].label, "account");
        assert_eq!(&text[output[1].start..output[1].end], "密😀码");
        assert_eq!(output[1].label, "common_secret");
        assert!(
            output
                .iter()
                .all(|span| span.score == 0.8125 && span.text_id == 7)
        );
    }

    #[test]
    fn url_query_is_never_discarded() {
        for text in [
            "nats://alice:password@%s?token=OTHER_SECRET",
            "nats://alice:password@%s/db#fragment",
            "nats://alice:password@db.internal:4222",
            "nats://alice:%s@%s",
        ] {
            let original = vec![span(0, text.len(), "url")];
            assert_eq!(refine(text, original.clone()).unwrap(), original, "{text}");
        }
    }

    #[test]
    fn category_c_table_is_sorted_and_matches_known_points() {
        assert!(
            unicode::CATEGORY_C
                .windows(2)
                .all(|pair| pair[0].1 < pair[1].0)
        );
        assert!(category_c('\u{0}'));
        assert!(category_c('\u{200b}'));
        assert!(category_c('\u{e000}'));
        assert!(!category_c('a'));
        assert!(!category_c('密'));
        assert!(!category_c('😀'));
    }

    fn parse_spans(value: &Value) -> Vec<DetectedSpan> {
        value
            .as_array()
            .expect("span array")
            .iter()
            .map(|span| DetectedSpan {
                text_id: u32::try_from(span["text_id"].as_u64().expect("text_id"))
                    .expect("text_id fits u32"),
                label: span["label"].as_str().expect("label").into(),
                start: span["start"].as_u64().expect("start") as usize,
                end: span["end"].as_u64().expect("end") as usize,
                score: span["score"].as_f64().expect("score") as f32,
            })
            .collect()
    }

    fn oracle_text(case: &Value) -> String {
        if let Some(text) = case["text"].as_str() {
            return text.to_owned();
        }
        let repeat = &case["text_repeat"];
        let mut text = repeat["unit"]
            .as_str()
            .expect("repeat unit")
            .repeat(repeat["count"].as_u64().expect("repeat count") as usize);
        text.push_str(repeat["tail"].as_str().expect("repeat tail"));
        text
    }

    // A synthetic subset (fixture, Unicode and IP-conformance cases) of the
    // AstrLink Guard `runs/span-worker-contract-v1` oracle, whose expected
    // spans come from the Python reference contracts.
    #[test]
    fn conforms_to_the_committed_synthetic_guard_oracle() {
        let mut lines = include_str!("../testdata/span-contract-v1-synthetic.jsonl").lines();
        let meta: Value = serde_json::from_str(lines.next().expect("meta line")).expect("meta");
        let mut cases = 0;
        let mut changed = 0;
        for line in lines {
            let case: Value = serde_json::from_str(line).expect("case");
            let input = parse_spans(&case["spans"]);
            let expected = parse_spans(&case["expected"]);
            changed += usize::from(input != expected);
            assert_eq!(
                refine(&oracle_text(&case), input),
                Ok(expected),
                "{}",
                case["id"]
            );
            cases += 1;
        }
        assert_eq!(Some(cases as u64), meta["meta"]["cases"].as_u64());
        assert!(changed > 500, "{changed}");
    }

    // The full 6,538-case oracle contains dataset text, so it stays outside
    // the repository. Run with
    // `ASTRLINK_SPAN_CONTRACT_ORACLE_DIR=<guard>/runs/span-worker-contract-v1
    // cargo test -- --ignored conforms_to_the_full_guard_oracle`.
    #[test]
    #[ignore = "requires ASTRLINK_SPAN_CONTRACT_ORACLE_DIR"]
    fn conforms_to_the_full_guard_oracle() {
        let directory = std::path::PathBuf::from(
            std::env::var_os("ASTRLINK_SPAN_CONTRACT_ORACLE_DIR")
                .expect("ASTRLINK_SPAN_CONTRACT_ORACLE_DIR"),
        );
        let read = |name: &str| {
            std::fs::read_to_string(directory.join(name))
                .unwrap_or_else(|error| panic!("{name}: {error}"))
        };
        let cases = read("cases.jsonl");
        let enabled = read("enabled.rust.predictions.jsonl");
        let disabled = read("disabled.rust.predictions.jsonl");
        let (mut cases, mut enabled, mut disabled) =
            (cases.lines(), enabled.lines(), disabled.lines());
        let mut count = 0;
        loop {
            let (case, enabled, disabled) = match (cases.next(), enabled.next(), disabled.next()) {
                (Some(case), Some(enabled), Some(disabled)) => (case, enabled, disabled),
                (None, None, None) => break,
                _ => panic!("oracle files differ in length after {count} cases"),
            };
            let case: Value = serde_json::from_str(case).expect("case");
            let enabled: Value = serde_json::from_str(enabled).expect("enabled prediction");
            let disabled: Value = serde_json::from_str(disabled).expect("disabled prediction");
            assert_eq!(case["id"], enabled["id"]);
            assert_eq!(case["id"], disabled["id"]);
            let input = parse_spans(&case["spans"]);
            assert_eq!(parse_spans(&disabled["spans"]), input, "{}", case["id"]);
            assert_eq!(
                refine(&oracle_text(&case), input),
                Ok(parse_spans(&enabled["spans"])),
                "{}",
                case["id"]
            );
            count += 1;
        }
        assert!(count > 0);
    }
}
