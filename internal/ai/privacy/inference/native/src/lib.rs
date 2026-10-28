//! In-process C ABI around AstrLink's privacy worker, retaining its tokenizer,
//! window stitching, decoder and span-boundary contracts.

mod bridge;
mod decoder;
mod engine;
mod manifest;
mod pplx;
mod priority;
mod protocol;
mod sensitive;
mod span_contract;
