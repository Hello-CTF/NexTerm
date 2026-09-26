//! 最小 portable-pty 验证：spawn cmd.exe /c echo，读取输出。
//! 运行：cargo run -p nexterm --example pty_min

use std::io::Read;
use std::time::{Duration, Instant};

fn main() {
    let pty_system = portable_pty::native_pty_system();
    let pair = pty_system
        .openpty(portable_pty::PtySize {
            rows: 30,
            cols: 120,
            pixel_width: 0,
            pixel_height: 0,
        })
        .expect("openpty");

    let mut cmd = portable_pty::CommandBuilder::new("cmd.exe");
    cmd.arg("/c");
    cmd.arg("echo HELLO_PTY");
    let _child = pair.slave.spawn_command(cmd).expect("spawn");
    let mut reader = pair.master.try_clone_reader().expect("reader");
    drop(pair.slave);

    let mut buf = [0u8; 4096];
    let start = Instant::now();
    let mut total = 0usize;
    while start.elapsed() < Duration::from_secs(5) {
        match reader.read(&mut buf) {
            Ok(0) => {
                println!("[EOF]");
                break;
            }
            Ok(n) => {
                total += n;
                print!("{}", String::from_utf8_lossy(&buf[..n]));
            }
            Err(e) => {
                println!("[read error: {e}]");
                break;
            }
        }
    }
    println!(
        "\n[total {total} bytes in {:.1}s]",
        start.elapsed().as_secs_f64()
    );
}
