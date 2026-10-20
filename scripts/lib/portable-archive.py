#!/usr/bin/env python3
import gzip
import os
from pathlib import Path
import shutil
import sys
import tarfile


def archive_paths(source, member):
    root = Path(source)
    base = root / member
    if not base.exists():
        raise ValueError(f"archive input does not exist: {base}")
    paths = [base]
    if base.is_dir():
        paths.extend(sorted(base.rglob("*"), key=lambda item: item.relative_to(root).as_posix()))
    return root, paths


def create_tar(artifact, source, member, epoch):
    root, paths = archive_paths(source, member)
    with open(artifact, "wb") as raw:
        with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=epoch) as compressed:
            with tarfile.open(fileobj=compressed, mode="w", format=tarfile.PAX_FORMAT) as archive:
                for item in paths:
                    relative = item.relative_to(root).as_posix()
                    info = archive.gettarinfo(str(item), arcname=relative)
                    info.uid = 0
                    info.gid = 0
                    info.uname = ""
                    info.gname = ""
                    info.mtime = epoch
                    if item.is_symlink() or (not item.is_file() and not item.is_dir()):
                        raise ValueError(f"archive input must be a regular file or directory: {item}")
                    if item.is_dir():
                        info.mode = 0o755
                        archive.addfile(info)
                    else:
                        info.mode = 0o755 if item.stat().st_mode & 0o111 else 0o644
                        with open(item, "rb") as handle:
                            archive.addfile(info, handle)
    os.chmod(artifact, 0o644)


def copy_tree(source, destination):
    source_path = Path(source)
    destination_path = Path(destination)
    destination_path.mkdir(parents=True, exist_ok=True)
    os.chmod(destination_path, 0o755)
    for item in sorted(source_path.rglob("*"), key=lambda value: value.relative_to(source_path).as_posix()):
        target = destination_path / item.relative_to(source_path)
        if item.is_symlink():
            raise ValueError(f"frontend output contains a symbolic link: {item}")
        if item.is_dir():
            target.mkdir(parents=True, exist_ok=True)
            os.chmod(target, 0o755)
        elif item.is_file():
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(item, target)
            os.chmod(target, 0o644)
        else:
            raise ValueError(f"frontend output contains a non-regular file: {item}")


def main():
    if len(sys.argv) == 6 and sys.argv[1] == "tar":
        create_tar(sys.argv[2], sys.argv[3], sys.argv[4], int(sys.argv[5]))
        return
    if len(sys.argv) == 4 and sys.argv[1] == "copy-tree":
        copy_tree(sys.argv[2], sys.argv[3])
        return
    raise ValueError("usage: portable-archive.py tar ARTIFACT SOURCE MEMBER EPOCH | copy-tree SOURCE DESTINATION")


if __name__ == "__main__":
    main()
