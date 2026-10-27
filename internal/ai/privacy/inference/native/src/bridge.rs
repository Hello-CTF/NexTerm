use std::{
    cell::Cell,
    ffi::{CStr, c_char, c_void},
    io,
    panic::{AssertUnwindSafe, catch_unwind},
    path::Path,
    ptr,
    sync::{
        Arc, Mutex, OnceLock,
        atomic::{AtomicBool, Ordering},
    },
};

use crate::manifest::{
    Adapter, Execution, ExecutionProvider, InputNames, ModelFile, ModelManifest, TagScheme,
};
use crate::{engine::PrivacyEngine, protocol::TextInput};
use ort::environment::Environment;
use serde::Deserialize;
use std::collections::BTreeMap;

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Installation {
    version: String,
    revision: String,
    precision: String,
    files: BTreeMap<String, FileChecksum>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct FileChecksum {
    size: u64,
    sha256: String,
}

pub(crate) fn load_manifest(directory: &Path) -> io::Result<ModelManifest> {
    // Tests from upstream use their original descriptor and bounded fixtures.
    #[cfg(test)]
    if directory.join("astrlink-model.json").is_file() {
        return ModelManifest::load(directory);
    }
    let path = directory.join("installation.json");
    let metadata = std::fs::symlink_metadata(&path)?;
    if metadata.file_type().is_symlink() || !metadata.is_file() || metadata.len() > 64 * 1024 {
        return Err(io::Error::other("invalid_model_manifest"));
    }
    let installation: Installation = serde_json::from_slice(&std::fs::read(path)?)
        .map_err(|_| io::Error::other("invalid_model_manifest"))?;
    if installation.version.is_empty() {
        return Err(io::Error::other("invalid_model_manifest"));
    }
    let model_path = match installation.precision.as_str() {
        "int8" => "model_int8.onnx",
        "fp32" => "model.onnx",
        "fp16" => "model_fp16_static512.onnx",
        _ => return Err(io::Error::other("unsupported_precision")),
    };
    let external_data_paths = if installation.precision == "fp16" {
        vec![]
    } else {
        vec![format!("{model_path}_data")]
    };
    let execution = (installation.precision == "fp16").then_some(Execution {
        provider: ExecutionProvider::Cpu,
        model_format: None,
        compute_units: None,
        static_sequence_length: 512,
        pad_token_id: 1,
    });
    let label_mapping = [
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
    ]
    .into_iter()
    .map(|label| (label.to_string(), Some(label.to_string())))
    .collect();
    let manifest = ModelManifest {
        version: 1,
        installation_id: "model_00000000000000000000000000000001".into(),
        identity: format!(
            "QuantumNous/astrlink-guard@{}#{}",
            installation.revision, installation.precision
        ),
        repo_id: "QuantumNous/astrlink-guard".into(),
        revision: installation.revision,
        variant_id: installation.precision,
        adapter: Adapter::HfTokenClassification,
        model_path: model_path.into(),
        external_data_paths,
        tokenizer_path: "tokenizer.json".into(),
        config_path: "config.json".into(),
        calibration_path: None,
        secret_rules_path: None,
        secret_calibration_path: None,
        tag_scheme: TagScheme::Bio,
        window: 512,
        stride: 128,
        max_request_tokens: 128 * 1024,
        input_names: InputNames {
            input_ids: "input_ids".into(),
            attention_mask: "attention_mask".into(),
            token_type_ids: Some("token_type_ids".into()),
        },
        output_name: "logits".into(),
        label_mapping,
        files: installation
            .files
            .into_iter()
            .map(|(path, file)| ModelFile {
                path,
                size: file.size,
                sha256: file.sha256,
            })
            .collect(),
        execution,
    };
    manifest.validate()?;
    Ok(manifest)
}

struct Handle {
    engine: Mutex<PrivacyEngine>,
    cancelled: AtomicBool,
}

static RUNTIME: OnceLock<(String, Arc<Environment>)> = OnceLock::new();
static INITIALIZE: Mutex<()> = Mutex::new(());
thread_local! { static CANCELLATION: Cell<*const AtomicBool> = const { Cell::new(ptr::null()) }; }

pub(crate) fn ensure_runtime_initialized() -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    #[cfg(test)]
    if RUNTIME.get().is_none() {
        let runtime = std::env::var("NEXTERM_PRIVACY_TEST_ORT").map_err(|_| {
            io::Error::other("set NEXTERM_PRIVACY_TEST_ORT to run native model tests")
        })?;
        initialize_runtime(&runtime)
            .map_err(|_| io::Error::other("runtime_initialization_failed"))?;
    }
    if RUNTIME.get().is_none() {
        return Err(io::Error::other("runtime_not_initialized").into());
    }
    Ok(())
}

fn initialize_runtime(runtime: &str) -> Result<(), i32> {
    if !Path::new(runtime).is_absolute() || !Path::new(runtime).is_file() {
        return Err(2);
    }
    let _lock = INITIALIZE.lock().map_err(|_| 5)?;
    if let Some((loaded, _)) = RUNTIME.get() {
        if loaded != runtime {
            return Err(2);
        }
    } else {
        // This library exposes only bounded status codes; a caught Rust panic
        // must not write arbitrary inference data to the application's stderr.
        std::panic::set_hook(Box::new(|_| {}));
        let _ = ort::init_from(runtime)
            .map_err(|_| 2)?
            .with_name("nexterm-privacy")
            .commit();
        let environment = ort::environment::get_environment().map_err(|_| 2)?;
        RUNTIME
            .set((runtime.to_string(), environment))
            .map_err(|_| 2)?;
    }
    Ok(())
}

pub(crate) fn check_cancelled() -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let cancelled = CANCELLATION.with(|slot| {
        let flag = slot.get();
        !flag.is_null() && unsafe { (*flag).load(Ordering::Acquire) }
    });
    if cancelled {
        return Err(io::Error::other("cancelled").into());
    }
    Ok(())
}

struct CancellationGuard;
impl Drop for CancellationGuard {
    fn drop(&mut self) {
        CANCELLATION.with(|slot| slot.set(ptr::null()));
    }
}

// Error codes are deliberately content-free: model text and runtime errors must
// not cross into application logs or telemetry.
fn status(f: impl FnOnce() -> Result<(), i32>) -> i32 {
    match catch_unwind(AssertUnwindSafe(f)) {
        Ok(Ok(())) => 0,
        Ok(Err(code)) => code,
        Err(_) => 5,
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn nexterm_privacy_abi() -> u32 {
    1
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn nexterm_privacy_open(
    directory: *const c_char,
    runtime: *const c_char,
    threads: u32,
    output: *mut *mut c_void,
) -> i32 {
    status(|| {
        if directory.is_null()
            || runtime.is_null()
            || output.is_null()
            || !(1..=64).contains(&threads)
        {
            return Err(1);
        }
        unsafe {
            *output = ptr::null_mut();
        }
        let directory = unsafe { CStr::from_ptr(directory) }
            .to_str()
            .map_err(|_| 1)?;
        let runtime = unsafe { CStr::from_ptr(runtime) }.to_str().map_err(|_| 1)?;
        initialize_runtime(runtime)?;
        let engine = PrivacyEngine::load(Path::new(directory), threads as usize).map_err(|_| 3)?;
        let handle = Box::new(Handle {
            engine: Mutex::new(engine),
            cancelled: AtomicBool::new(false),
        });
        unsafe {
            *output = Box::into_raw(handle).cast();
        }
        Ok(())
    })
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn nexterm_privacy_detect(
    handle: *mut c_void,
    text: *const u8,
    length: usize,
    output: *mut *mut u8,
    output_length: *mut usize,
) -> i32 {
    status(|| {
        if handle.is_null()
            || text.is_null()
            || output.is_null()
            || output_length.is_null()
            || length > 16 * 1024 * 1024
        {
            return Err(1);
        }
        unsafe {
            *output = ptr::null_mut();
            *output_length = 0;
        }
        let handle = unsafe { &*handle.cast::<Handle>() };
        let mut engine = handle.engine.lock().map_err(|_| 5)?;
        CANCELLATION.with(|slot| slot.set(&handle.cancelled));
        let _guard = CancellationGuard;
        check_cancelled().map_err(|_| 6)?;
        let text = unsafe { std::slice::from_raw_parts(text, length) };
        let text = std::str::from_utf8(text).map_err(|_| 1)?;
        let spans = engine
            .detect(&[TextInput {
                id: 0,
                text: text.to_string(),
            }])
            .map_err(|error| match error.to_string().as_str() {
                "token_limit_exceeded" => 4,
                "cancelled" => 6,
                _ => 5,
            })?;
        check_cancelled().map_err(|_| 6)?;
        let bytes = serde_json::to_vec(&spans)
            .map_err(|_| 5)?
            .into_boxed_slice();
        unsafe {
            *output_length = bytes.len();
            *output = Box::into_raw(bytes).cast::<u8>();
        }
        Ok(())
    })
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn nexterm_privacy_reset(handle: *mut c_void) {
    if !handle.is_null() {
        unsafe { &*handle.cast::<Handle>() }
            .cancelled
            .store(false, Ordering::Release);
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn nexterm_privacy_cancel(handle: *mut c_void) {
    if !handle.is_null() {
        unsafe { &*handle.cast::<Handle>() }
            .cancelled
            .store(true, Ordering::Release);
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn nexterm_privacy_free(data: *mut u8, length: usize) {
    if !data.is_null() {
        drop(unsafe { Box::from_raw(ptr::slice_from_raw_parts_mut(data, length)) });
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn nexterm_privacy_close(handle: *mut c_void) {
    if !handle.is_null() {
        drop(unsafe { Box::from_raw(handle.cast::<Handle>()) });
    }
}
