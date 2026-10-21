#!/usr/bin/env python3
"""把 OCI 布局的镜像 tar 转成传统 docker-archive 布局。

Docker 25+ 在 containerd 存储驱动下，`docker save` 与 buildx 的 docker exporter
都写 OCI 布局（blobs/sha256/... + index.json）。而 NAS 上较老的 Docker（20.10 /
24.x）只认经典布局（manifest.json + 每层一个目录），导入会直接报格式错误。

用法：
    python3 oci2docker.py 输入.tar 输出.tar [tag]

输入已是经典布局时原样复制；只依赖标准库。
"""

import gzip
import json
import os
import shutil
import sys
import tarfile


def read_member(tf, name):
    f = tf.extractfile(name)
    if f is None:
        raise SystemExit(f"tar 内缺少 {name}")
    return f.read()


def load_json(tf, name):
    return json.loads(read_member(tf, name))


def blob_name(digest):
    return "blobs/sha256/" + digest.split(":", 1)[1]


def resolve_manifest(tf, desc):
    """index.json 里可能指向镜像清单，也可能指向多平台索引，向下取一层。"""
    data = load_json(tf, blob_name(desc["digest"]))
    if "manifests" in data:
        if not data["manifests"]:
            raise SystemExit("多平台索引是空的")
        return resolve_manifest(tf, data["manifests"][0])
    return data


def main():
    if len(sys.argv) < 3:
        print(__doc__.strip().splitlines()[0], file=sys.stderr)
        print("用法: oci2docker.py 输入.tar 输出.tar [tag]", file=sys.stderr)
        return 2

    src_path, dst_path = sys.argv[1], sys.argv[2]
    tag = sys.argv[3] if len(sys.argv) > 3 else "wol:1.0.0"

    with tarfile.open(src_path, "r:*") as src:
        names = set(src.getnames())

        # 有 index.json 就是 OCI 布局（可能同时带一个指向 blobs/ 的 manifest.json，
        # 那种混合布局老版本 Docker 仍可能拒绝，统一转干净）。
        if "index.json" not in names:
            if "manifest.json" in names:
                print("已是传统 docker-archive 布局，无需转换")
                if os.path.abspath(src_path) != os.path.abspath(dst_path):
                    shutil.copyfile(src_path, dst_path)
                return 0
            raise SystemExit("既不是 OCI 也不是 docker-archive，无法识别")

        index = load_json(src, "index.json")
        if not index.get("manifests"):
            raise SystemExit("index.json 里没有镜像清单")
        manifest = resolve_manifest(src, index["manifests"][0])

        config_digest = manifest["config"]["digest"]
        config_hex = config_digest.split(":", 1)[1]
        config_bytes = read_member(src, blob_name(config_digest))
        config = json.loads(config_bytes)

        layers = []
        tmp_dir = dst_path + ".layers"
        os.makedirs(tmp_dir, exist_ok=True)

        # 逐层解压：docker-archive 的 layer.tar 必须未压缩。
        for i, layer in enumerate(manifest["layers"]):
            raw = read_member(src, blob_name(layer["digest"]))
            if raw[:2] == b"\x1f\x8b":
                raw = gzip.decompress(raw)
            layer_hex = layer["digest"].split(":", 1)[1]
            layer_dir = os.path.join(tmp_dir, layer_hex)
            os.makedirs(layer_dir, exist_ok=True)
            with open(os.path.join(layer_dir, "layer.tar"), "wb") as out:
                out.write(raw)
            layers.append(layer_hex)
            print(f"  层 {i + 1}/{len(manifest['layers'])} {len(raw) / 1048576:.1f} MB")

        if os.path.exists(dst_path):
            os.remove(dst_path)

        with tarfile.open(dst_path, "w") as dst:
            def add_bytes(name, data):
                info = tarfile.TarInfo(name)
                info.size = len(data)
                info.mtime = 0
                info.mode = 0o644
                dst.addfile(info, __import__("io").BytesIO(data))

            add_bytes(config_hex + ".json", config_bytes)
            for layer_hex in layers:
                path = os.path.join(tmp_dir, layer_hex, "layer.tar")
                dst.add(path, arcname=layer_hex + "/layer.tar")

            manifest_json = [{
                "Config": config_hex + ".json",
                "RepoTags": [tag],
                "Layers": [h + "/layer.tar" for h in layers],
            }]
            add_bytes("manifest.json", json.dumps(manifest_json).encode())

        shutil.rmtree(tmp_dir, ignore_errors=True)
        print(f"转换完成：{config.get('os', '?')}/{config.get('architecture', '?')}，{len(layers)} 层")
        return 0


if __name__ == "__main__":
    sys.exit(main())
