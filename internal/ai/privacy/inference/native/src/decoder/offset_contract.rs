//! Opt-in `bio-offset-consistency-v1` decoder contract for AstrLink Guard models.
//!
//! Ported from AstrLink Guard `tools/offset-worker/contract.rs` (sha256
//! f9dffaa1180d8616d6a715c1dd4d28feaeca891dc1dbab9e02205fb0bfd761d5), which
//! mirrors `astrlink_guard/consistent_decode.py`. Tokens whose offsets start
//! before the furthest end seen so far share characters with an earlier token;
//! the path may not open, close or switch an entity inside such a token, so
//! every entity boundary falls on a character boundary. Entity ends are the
//! union of their tokens' ends.

use serde_json::Value;

use super::{Decoder, Entity, Tag};
use crate::{manifest::TagScheme, protocol::DetectedSpan};

pub const CONFIG_FIELD: &str = "astrlink_guard_decoder_contract";
pub const CONTRACT: &str = "bio-offset-consistency-v1";

const KINDS: [&str; 10] = [
    "email",
    "phone",
    "account",
    "payment_card",
    "ip_address",
    "url",
    "common_secret",
    "private_address",
    "private_date",
    "private_person",
];
const LABEL_COUNT: usize = 1 + 2 * KINDS.len();

/// Absent or null keeps the legacy decoder; any other value than the known
/// contract fails model loading.
pub fn configured(config: &[u8]) -> Result<bool, &'static str> {
    let config: Value = serde_json::from_slice(config).map_err(|_| "invalid_model_config")?;
    match config.get(CONFIG_FIELD) {
        None | Some(Value::Null) => Ok(false),
        Some(Value::String(value)) if value == CONTRACT => Ok(true),
        _ => Err("invalid_decoder_contract"),
    }
}

impl Decoder {
    /// Enables the contract; it is defined only for the fixed 21-label BIO
    /// identity map `O, B-/I-` for each Guard kind in [`KINDS`] order.
    pub fn with_offset_consistency(mut self) -> Result<Self, &'static str> {
        if self.scheme != TagScheme::Bio
            || self.labels.len() != LABEL_COUNT
            || self.labels.first() != Some(&Tag::Outside)
        {
            return Err("invalid_decoder_contract");
        }
        for (index, kind) in KINDS.iter().enumerate() {
            let entity = Entity {
                source: (*kind).into(),
                canonical: Some((*kind).into()),
            };
            if self.labels[1 + index * 2] != Tag::Begin(entity.clone())
                || self.labels[2 + index * 2] != Tag::Inside(entity)
            {
                return Err("invalid_decoder_contract");
            }
        }
        self.offset_consistency = true;
        Ok(self)
    }

    pub(super) fn viterbi_offset_consistent(
        &self,
        logits: &[f32],
        offsets: &[(usize, usize)],
    ) -> Result<Vec<usize>, &'static str> {
        let count = offsets.len();
        let width = self.labels.len();
        if count == 0 {
            return Ok(Vec::new());
        }
        for (index, &(start, end)) in offsets.iter().enumerate() {
            if end <= start || (index > 0 && start < offsets[index - 1].0) {
                return Err("invalid_offsets");
            }
        }
        let size = count.checked_mul(width).ok_or("invalid_logits_shape")?;
        let mut backpointers = vec![0_u8; size];
        let mut scores = logits[..width].to_vec();
        // Even label ids are I-* tags, which cannot open a path.
        for score in scores.iter_mut().skip(2).step_by(2) {
            *score = f32::NEG_INFINITY;
        }
        let mut next_scores = vec![f32::NEG_INFINITY; width];
        let mut furthest_end = offsets[0].1;
        for token in 1..count {
            let overlap = offsets[token].0 < furthest_end;
            next_scores.fill(f32::NEG_INFINITY);
            let emissions = &logits[token * width..(token + 1) * width];
            let parents = &mut backpointers[token * width..(token + 1) * width];
            for (target, ((next, emission), parent)) in next_scores
                .iter_mut()
                .zip(emissions)
                .zip(parents.iter_mut())
                .enumerate()
            {
                let inside = target > 0 && target.is_multiple_of(2);
                let mut best = f32::NEG_INFINITY;
                let mut best_previous = 0;
                for (previous, &value) in scores.iter().enumerate() {
                    let allowed = if overlap {
                        (previous == 0 && target == 0)
                            || (inside && (previous == target || previous + 1 == target))
                    } else {
                        !inside || previous == target || previous + 1 == target
                    };
                    if allowed && value > best {
                        best = value;
                        best_previous = previous;
                    }
                }
                // Add the emission after the argmax, exactly like the Python
                // float32 reference Viterbi.
                *next = best + emission;
                *parent = best_previous as u8;
            }
            if next_scores.contains(&f32::INFINITY)
                || !next_scores.iter().any(|value| value.is_finite())
            {
                return Err("nonfinite_path_score");
            }
            std::mem::swap(&mut scores, &mut next_scores);
            furthest_end = furthest_end.max(offsets[token].1);
        }
        // The reference final-state tie rule: the last maximal label wins.
        let mut last = 0;
        for (state, score) in scores.iter().enumerate().skip(1) {
            if *score >= scores[last] {
                last = state;
            }
        }
        if !scores[last].is_finite() {
            return Err("nonfinite_path_score");
        }
        let mut path = vec![0; count];
        path[count - 1] = last;
        for token in (1..count).rev() {
            path[token - 1] = usize::from(backpointers[token * width + path[token]]);
        }
        Ok(path)
    }
}

pub(super) fn decode_offset_spans(
    text_id: u32,
    labels: &[Tag],
    path: &[usize],
    offsets: &[(usize, usize)],
    probabilities: &[f32],
) -> Result<Vec<DetectedSpan>, &'static str> {
    let mut spans = Vec::new();
    let mut index = 0;
    while index < path.len() {
        match &labels[path[index]] {
            Tag::Outside => index += 1,
            Tag::Begin(entity) => {
                let first = index;
                index += 1;
                while index < path.len()
                    && matches!(&labels[path[index]], Tag::Inside(next) if next == entity)
                {
                    index += 1;
                }
                let end = offsets[first..index]
                    .iter()
                    .map(|(_, end)| *end)
                    .max()
                    .ok_or("invalid_offsets")?;
                spans.push(DetectedSpan {
                    text_id,
                    label: entity.canonical.clone().ok_or("invalid_decoder_contract")?,
                    start: offsets[first].0,
                    end,
                    score: probabilities[first..index].iter().copied().sum::<f32>()
                        / (index - first) as f32,
                });
            }
            _ => return Err("invalid_decoded_path"),
        }
    }
    Ok(spans)
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeMap;

    use super::*;

    fn config(contract: Option<&str>) -> Vec<u8> {
        let mut labels = BTreeMap::from([("0".to_owned(), "O".to_owned())]);
        for (index, kind) in KINDS.iter().enumerate() {
            labels.insert((1 + index * 2).to_string(), format!("B-{kind}"));
            labels.insert((2 + index * 2).to_string(), format!("I-{kind}"));
        }
        let mut value = serde_json::json!({ "id2label": labels });
        if let Some(contract) = contract {
            value[CONFIG_FIELD] = contract.into();
        }
        serde_json::to_vec(&value).expect("serialize config")
    }

    fn mapping() -> BTreeMap<String, Option<String>> {
        KINDS
            .iter()
            .map(|kind| ((*kind).to_owned(), Some((*kind).to_owned())))
            .collect()
    }

    fn decoder(contract: bool) -> Decoder {
        let decoder =
            Decoder::from_hf_json(&config(None), TagScheme::Bio, &mapping()).expect("HF decoder");
        if contract {
            decoder.with_offset_consistency().expect("offset contract")
        } else {
            decoder
        }
    }

    #[test]
    fn configuration_is_optional_and_strict() {
        assert_eq!(configured(&config(None)), Ok(false));
        assert_eq!(
            configured(br#"{"astrlink_guard_decoder_contract":null}"#),
            Ok(false)
        );
        assert_eq!(configured(&config(Some(CONTRACT))), Ok(true));
        for invalid in [
            br#"{"astrlink_guard_decoder_contract":"bio-offset-consistency-v2"}"#.as_slice(),
            br#"{"astrlink_guard_decoder_contract":""}"#,
            br#"{"astrlink_guard_decoder_contract":true}"#,
            br#"{"astrlink_guard_decoder_contract":["bio-offset-consistency-v1"]}"#,
        ] {
            assert_eq!(configured(invalid), Err("invalid_decoder_contract"));
        }
        assert_eq!(configured(b"not json"), Err("invalid_model_config"));
    }

    #[test]
    fn opt_in_retains_legacy_and_corrects_shared_character() {
        let mut scores = vec![-40.0; 42];
        scores[13] = 10.0;
        scores[21] = 4.0;
        scores[21 + 14] = 3.0;
        let offsets = [(0, 1), (0, 3)];
        assert_eq!(
            decoder(false).decode(0, &scores, &offsets).unwrap()[0].end,
            1
        );
        assert_eq!(
            decoder(true).decode(0, &scores, &offsets).unwrap()[0].end,
            3
        );
    }

    #[test]
    fn strong_background_can_remove_fragment() {
        let mut scores = vec![-40.0; 42];
        scores[0] = 0.0;
        scores[13] = 2.0;
        scores[21] = 10.0;
        scores[21 + 14] = 0.0;
        assert!(
            decoder(true)
                .decode(0, &scores, &[(0, 1), (0, 4)])
                .unwrap()
                .is_empty()
        );
    }

    #[test]
    fn empty_nonmonotonic_and_nonfinite_offsets_fail() {
        assert_eq!(
            decoder(true)
                .decode(0, &[0.0; 63], &[(0, 3), (1, 1), (2, 3)])
                .unwrap_err(),
            "invalid_offsets"
        );
        assert_eq!(
            decoder(true)
                .decode(0, &[0.0; 42], &[(2, 3), (1, 4)])
                .unwrap_err(),
            "invalid_offsets"
        );
        assert!(decoder(true).decode(0, &[f32::NAN; 21], &[(0, 1)]).is_err());
    }

    #[test]
    fn nested_offsets_take_union_end_and_adjacent_entities_remain_separate() {
        let mut scores = vec![-40.0; 84];
        for (token, label) in [5, 6, 6, 5].into_iter().enumerate() {
            scores[token * 21 + label] = 10.0;
        }
        let spans = decoder(true)
            .decode(0, &scores, &[(0, 4), (1, 2), (3, 4), (4, 5)])
            .unwrap();
        assert_eq!(
            spans
                .iter()
                .map(|span| (span.start, span.end, span.label.as_str()))
                .collect::<Vec<_>>(),
            vec![(0, 4, "account"), (4, 5, "account")]
        );
        assert!(spans.iter().all(|span| span.text_id == 0));
    }

    #[test]
    fn score_is_mean_token_probability_and_final_ties_take_the_last_label() {
        let mut scores = vec![-40.0; 42];
        scores[1] = 0.0;
        // `O` and `I-email` tie on the last token; the later label wins.
        scores[21] = 0.0;
        scores[21 + 2] = 0.0;
        let spans = decoder(true).decode(9, &scores, &[(0, 2), (2, 4)]).unwrap();
        assert_eq!(spans.len(), 1);
        assert_eq!(
            (
                spans[0].text_id,
                spans[0].start,
                spans[0].end,
                spans[0].label.as_str()
            ),
            (9, 0, 4, "email")
        );
        assert!((spans[0].score - 0.75).abs() < 1e-6);
    }

    #[test]
    fn contract_rejects_bioes_and_changed_label_map() {
        let bioes = Decoder::from_hf_json(
            &serde_json::to_vec(&serde_json::json!({"id2label": {
                "0": "O", "1": "B-email", "2": "I-email", "3": "E-email", "4": "S-email"
            }}))
            .unwrap(),
            TagScheme::Bioes,
            &BTreeMap::from([("email".to_owned(), Some("email".to_owned()))]),
        )
        .expect("BIOES decoder");
        assert_eq!(
            bioes.with_offset_consistency().unwrap_err(),
            "invalid_decoder_contract"
        );
        let mut changed = mapping();
        changed.insert("common_secret".into(), None);
        let decoder = Decoder::from_hf_json(&config(None), TagScheme::Bio, &changed)
            .expect("HF decoder with null mapping");
        assert_eq!(
            decoder.with_offset_consistency().unwrap_err(),
            "invalid_decoder_contract"
        );
        let mut renamed = mapping();
        renamed.insert("email".into(), Some("url".into()));
        let decoder = Decoder::from_hf_json(&config(None), TagScheme::Bio, &renamed)
            .expect("HF decoder with renamed mapping");
        assert_eq!(
            decoder.with_offset_consistency().unwrap_err(),
            "invalid_decoder_contract"
        );
    }
}
