//! The weights file. The format is the one of the deepblue engine for `gcnii_star` (baby_thinker), so files
//! trained there load here and the other way round.
//!
//! All integers little-endian u32: the magic `GCN*` (4 bytes), the raw feature count (rows of X), classes,
//! hidden, depth; then the node type vocabulary: the number of types and, for each, its byte length and its
//! UTF-8 bytes; then all parameters as little-endian f32, in the order of `GcniiStar::params`.
//! The shape of the parameters is derived from the header.

use super::features::TypeVocab;
use super::math::Rng;
use super::model::{GcniiStar, ModelConfig};
use std::fs;
use std::path::Path;

/// Never change it, or the weights files stop loading. (The vanilla GCNII of deepblue uses `GCN2`, not supported.)
pub const MAGIC: [u8; 4] = *b"GCN*";

const HEADER_BYTES: usize = 20;

/// The file contents of the model.
pub fn encode(m: &GcniiStar) -> Vec<u8> {
    let ModelConfig {
        classes,
        hidden,
        depth,
    } = m.cfg;
    let header = [m.vocab.width(), classes, hidden, depth, m.vocab.types().len()]
        .into_iter()
        .flat_map(|v| (v as u32).to_le_bytes());
    let types = m.vocab.types().iter().flat_map(|t| {
        (t.len() as u32)
            .to_le_bytes()
            .into_iter()
            .chain(t.as_bytes().iter().copied())
    });
    let params = m.params.iter().flatten().flat_map(|w| w.to_le_bytes());
    MAGIC
        .into_iter()
        .chain(header)
        .chain(types)
        .chain(params)
        .collect()
}

pub fn save(m: &GcniiStar, path: &Path) -> Result<(), String> {
    if let Some(dir) = path.parent().filter(|d| !d.as_os_str().is_empty()) {
        fs::create_dir_all(dir).map_err(|err| format!("{}: {err}", dir.display()))?;
    }
    fs::write(path, encode(m)).map_err(|err| format!("{}: {err}", path.display()))
}

pub fn load(path: &Path) -> Result<GcniiStar, String> {
    let bytes = fs::read(path).map_err(|err| format!("{}: {err}", path.display()))?;
    decode(&bytes).map_err(|err| format!("{}: {err}", path.display()))
}

/// Parses the contents of a weights file; the model size and the type vocabulary come from its header.
pub fn decode(bytes: &[u8]) -> Result<GcniiStar, String> {
    if bytes.len() < HEADER_BYTES + 4 || bytes[..4] != MAGIC {
        return Err("not a GCNII* weights file (magic GCN*)".to_string());
    }
    let u32_at = |off: usize| -> Result<usize, String> {
        bytes
            .get(off..off + 4)
            .map(|b| u32::from_le_bytes([b[0], b[1], b[2], b[3]]) as usize)
            .ok_or_else(|| "truncated header".to_string())
    };
    let cfg = ModelConfig {
        classes: u32_at(8)?,
        hidden: u32_at(12)?,
        depth: u32_at(16)?,
    };
    if cfg.classes < 1 || cfg.hidden < 1 || cfg.depth < 1 {
        return Err(format!("bad header {cfg:?}"));
    }
    let type_count = u32_at(HEADER_BYTES)?;
    let (types, off) = (0..type_count).try_fold(
        (Vec::new(), HEADER_BYTES + 4),
        |(mut types, off), _| -> Result<(Vec<String>, usize), String> {
            let len = u32_at(off)?;
            let raw = bytes
                .get(off + 4..off + 4 + len)
                .ok_or("truncated type vocabulary")?;
            types.push(String::from_utf8(raw.to_vec()).map_err(|_| "type vocabulary is not UTF-8")?);
            Ok((types, off + 4 + len))
        },
    )?;
    let vocab = TypeVocab::new(types);
    if u32_at(4)? != vocab.width() {
        return Err(format!(
            "{} input features, but {} types need {}",
            u32_at(4)?,
            vocab.types().len(),
            vocab.width()
        ));
    }
    // a model only for the shapes; the values are overwritten
    let shaped = GcniiStar::new(cfg, vocab, &mut Rng::new(1));
    let want: usize = shaped.params.iter().map(|p| p.len() * 4).sum();
    if bytes.len() - off != want {
        return Err(format!(
            "{} bytes of weights, the header {cfg:?} needs {want}",
            bytes.len() - off
        ));
    }
    let values: Vec<f32> = bytes[off..]
        .chunks_exact(4)
        .map(|b| f32::from_le_bytes([b[0], b[1], b[2], b[3]]))
        .collect();
    let params = shaped
        .params
        .iter()
        .scan(0usize, |start, p| {
            let slice = values[*start..*start + p.len()].to_vec();
            *start += p.len();
            Some(slice)
        })
        .collect();
    Ok(GcniiStar { params, ..shaped })
}

#[cfg(test)]
mod tests {
    use super::super::model::tests::setup;
    use super::*;

    #[test]
    fn roundtrip_gives_the_same_outputs() {
        let (m, g, x) = setup();
        let back = decode(&encode(&m)).unwrap();
        assert_eq!(back.cfg, m.cfg);
        assert_eq!(back.vocab, m.vocab);
        assert_eq!(back.logits(&g, &x), m.logits(&g, &x));
    }

    #[test]
    fn wrong_magic_and_truncation_are_errors() {
        let (m, _, _) = setup();
        let mut bytes = encode(&m);
        assert!(decode(&bytes[..bytes.len() - 4]).is_err());
        bytes[3] = b'2';
        assert!(decode(&bytes).is_err());
    }
}
