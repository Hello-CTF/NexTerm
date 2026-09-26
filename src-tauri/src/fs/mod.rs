//! 文件传输（M1-T6）：上传 / 下载 + 进度事件 + 断点续传。
pub mod mount;

use serde_json::json;

use crate::error::AppResult;
use crate::ids::new_id;
use crate::state::AppState;

/// 上传本地文件 → 远端路径（200ms 间隔推 fs://progress）。
pub async fn upload(
    state: &AppState,
    session_id: &str,
    local_path: String,
    remote_path: String,
    resume: bool,
) -> AppResult<u64> {
    let session = state.sessions.get(session_id).await?;
    let fs = session.transport().await.fs().await?;
    let total = tokio::fs::metadata(&local_path).await?.len();
    let task_id = new_id();

    // 断点续传：远端已存在时从已有大小继续
    let mut start: u64 = 0;
    if resume {
        if let Ok(existing) = fs.size(&remote_path).await {
            if existing > 0 && existing < total {
                start = existing;
            }
        }
    }

    let mut local = tokio::fs::File::open(&local_path).await?;
    use tokio::io::{AsyncReadExt, AsyncSeekExt};
    local.seek(std::io::SeekFrom::Start(start)).await?;
    let mut remote = fs.open_write(&remote_path, start > 0).await?;

    let mut buffer = vec![0u8; 256 * 1024];
    let mut transferred = start;
    let mut last_report = std::time::Instant::now();
    loop {
        let n = local.read(&mut buffer).await?;
        if n == 0 {
            break;
        }
        remote.write(&buffer[..n]).await?;
        transferred += n as u64;
        if last_report.elapsed() > std::time::Duration::from_millis(200) {
            report(state, &task_id, transferred, total, false);
            last_report = std::time::Instant::now();
        }
    }
    remote.finish().await?;
    report(state, &task_id, transferred, total, true);
    Ok(transferred)
}

/// 下载远端文件 → 本地路径。
pub async fn download(
    state: &AppState,
    session_id: &str,
    remote_path: String,
    local_path: String,
) -> AppResult<u64> {
    let session = state.sessions.get(session_id).await?;
    let fs = session.transport().await.fs().await?;
    let task_id = new_id();

    let mut remote = fs.open_read(&remote_path).await?;
    let total = remote.size();
    let mut local = tokio::fs::File::create(&local_path).await?;
    use tokio::io::AsyncWriteExt;

    let mut buffer = vec![0u8; 256 * 1024];
    let mut transferred: u64 = 0;
    let mut last_report = std::time::Instant::now();
    loop {
        let n = remote.read(&mut buffer).await?;
        if n == 0 {
            break;
        }
        local.write_all(&buffer[..n]).await?;
        transferred += n as u64;
        if last_report.elapsed() > std::time::Duration::from_millis(200) {
            report(state, &task_id, transferred, total, false);
            last_report = std::time::Instant::now();
        }
    }
    local.flush().await?;
    report(state, &task_id, transferred, total, true);
    Ok(transferred)
}

fn report(state: &AppState, task_id: &str, transferred: u64, total: u64, done: bool) {
    use tauri::Emitter;
    let _ = state.app.emit(
        crate::events::FS_PROGRESS,
        json!({
            "taskId": task_id,
            "transferred": transferred,
            "total": total,
            "done": done,
        }),
    );
}
