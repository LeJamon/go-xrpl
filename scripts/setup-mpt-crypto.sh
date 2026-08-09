#!/usr/bin/env bash
set -euo pipefail

# The mpt-crypto package is a C++ static library. Keep Conan state inside the
# checkout so configuring the XRPLF remote never changes a user's Conan home.

readonly script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly repo_root="$(cd -- "$script_dir/.." && pwd)"
readonly remote_name="${GOXRPL_MPT_CRYPTO_REMOTE_NAME:-xrplf}"
readonly remote_url="${GOXRPL_MPT_CRYPTO_REMOTE_URL:-https://conan.xrplf.org/repository/conan/}"
readonly conan_storage_home="${GOXRPL_MPT_CRYPTO_CONAN_HOME:-$repo_root/.conan-home}"
readonly output_dir="${GOXRPL_MPT_CRYPTO_OUTPUT_DIR:-$repo_root/.mpt-crypto}"
readonly requirements_file="$repo_root/conan-mpt-crypto.txt"
readonly lock_file="$repo_root/conan-mpt-crypto.lock"
conan_home="$conan_storage_home"

usage() {
    printf 'Usage: %s {setup|env|test} [package]\n' "$(basename "$0")" >&2
    printf '\n' >&2
    printf '  setup  Install the locked mpt-crypto graph and generate PkgConfigDeps.\n' >&2
    printf '  env    Print shell exports for pkg-config and optional C++ overrides.\n' >&2
    printf '  test   Run upstream 1.0.2 tests, validate linking, then run tagged Go tests.\n' >&2
}

die() {
    printf 'setup-mpt-crypto: %s\n' "$*" >&2
    exit 1
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

require_files() {
    [[ -f "$requirements_file" ]] || die "missing requirements file: $requirements_file"
    [[ -f "$lock_file" ]] || die "missing lock file: $lock_file"
}

prepare_conan_home() {
    mkdir -p "$conan_storage_home"
    local physical_home
    physical_home="$(cd -- "$conan_storage_home" && pwd -P)"
    conan_home="$physical_home"

    if [[ "$physical_home" != *[[:space:]]* ]]; then
        return
    fi

    # The pinned OpenSSL recipe does not quote dependency include paths while
    # building from source. Keep its storage project-local, but give Conan a
    # stable no-space path to that same directory.
    local alias_root="/tmp/goxrpl-mpt-crypto-$(id -u)"
    if [[ -L "$alias_root" ]]; then
        die "unsafe Conan alias root is a symlink: $alias_root"
    fi
    mkdir -p "$alias_root"
    chmod 700 "$alias_root"

    local checksum remainder
    read -r checksum remainder < <(printf '%s' "$physical_home" | cksum)
    local alias_home="$alias_root/conan-$checksum"
    if [[ -e "$alias_home" || -L "$alias_home" ]]; then
        [[ -L "$alias_home" ]] || die "Conan alias exists and is not a symlink: $alias_home"
        [[ "$(readlink "$alias_home")" == "$physical_home" ]] || die \
            "Conan alias points at an unexpected directory: $alias_home"
    else
        ln -s "$physical_home" "$alias_home"
    fi
    conan_home="$alias_home"
}

ensure_remote() {
    mkdir -p "$conan_home"
    CONAN_HOME="$conan_home" conan remote add \
        --force \
        --index 0 \
        "$remote_name" \
        "$remote_url" >/dev/null
}

ensure_profile() {
    CONAN_HOME="$conan_home" conan profile detect --force >/dev/null

    local profile="$conan_home/profiles/default"
    local compiler compiler_version
    compiler="$(awk -F= '$1 == "compiler" { print $2; exit }' "$profile")"
    compiler_version="$(awk -F= '$1 == "compiler.version" { print $2; exit }' "$profile")"

    # Conan 2.18 recognizes Apple Clang through version 17. Newer Apple Clang
    # releases remain compatible with that settings model but must be capped
    # before Conan validates the generated profile.
    if [[ "$compiler" == "apple-clang" && "$compiler_version" =~ ^([0-9]+) ]] &&
        (( BASH_REMATCH[1] > 17 )); then
        local normalized_profile
        normalized_profile="$(mktemp "${profile}.XXXXXX")"
        awk '$0 ~ /^compiler.version=/ { print "compiler.version=17"; next } { print }' \
            "$profile" > "$normalized_profile"
        mv "$normalized_profile" "$profile"
    fi
}

install_graph() {
    require_command conan
    require_command pkg-config
    require_files
    ensure_remote
    ensure_profile
    mkdir -p "$output_dir"

    # The recipe is built with C++23 by XRPLF. Override only the host language
    # standard; the detected compiler/libcxx/OS/architecture remain native to
    # the current macOS or Linux host.
    CONAN_HOME="$conan_home" conan install "$requirements_file" \
        --lockfile "$lock_file" \
        --output-folder "$output_dir" \
        --profile:host default \
        --profile:build default \
        --settings:host compiler.cppstd=23 \
        --build='*' \
        --remote "$remote_name"

    [[ -f "$output_dir/mpt-crypto.pc" ]] || die \
        "Conan did not generate $output_dir/mpt-crypto.pc"
    [[ -f "$output_dir/secp256k1.pc" ]] || die \
        "Conan did not generate $output_dir/secp256k1.pc"

    # The existing secp cgo shim uses the upstream pkg-config package name.
    # Alias it to Conan's package so tagged builds cannot mix system and Conan
    # copies of libsecp256k1 in one process.
    cp "$output_dir/secp256k1.pc" "$output_dir/libsecp256k1.pc"

    # mpt-crypto and the existing secp shim are separate cgo packages. The shim
    # owns the secp link flag; this metadata keeps mpt-crypto's headers and
    # remaining dependencies without adding a second copy of that flag.
    local mpt_prefix mpt_libdir mpt_includedir mpt_version secp_includedir
    mpt_prefix="$(PKG_CONFIG_PATH="$output_dir" pkg-config --variable=prefix mpt-crypto)"
    mpt_libdir="$(PKG_CONFIG_PATH="$output_dir" pkg-config --variable=libdir mpt-crypto)"
    mpt_includedir="$(PKG_CONFIG_PATH="$output_dir" pkg-config --variable=includedir mpt-crypto)"
    mpt_version="$(PKG_CONFIG_PATH="$output_dir" pkg-config --modversion mpt-crypto)"
    secp_includedir="$(PKG_CONFIG_PATH="$output_dir" pkg-config --variable=includedir secp256k1)"
    {
        printf 'prefix=%s\n' "$mpt_prefix"
        printf 'libdir=%s\n' "$mpt_libdir"
        printf 'includedir=%s\n' "$mpt_includedir"
        printf '\nName: goxrpl-mpt-crypto\n'
        printf 'Description: mpt-crypto linked with the project secp shim\n'
        printf 'Version: %s\n' "$mpt_version"
        printf 'Libs: -L"${libdir}" -lmpt-crypto\n'
        printf 'Cflags: -I"${includedir}" -I"%s"\n' "$secp_includedir"
        printf 'Requires: openssl\n'
    } > "$output_dir/goxrpl-mpt-crypto.pc"
}

profile_libcxx() {
    local profile="$conan_home/profiles/default"
    if [[ -f "$profile" ]]; then
        awk -F= '$1 == "compiler.libcxx" { print $2; exit }' "$profile"
    fi
}

cxx_runtime_flags() {
    printf '%s\n' "${GOXRPL_MPT_CRYPTO_CXX_RUNTIME:-}"
}

cxx_compile_flags() {
    if [[ -n "${GOXRPL_MPT_CRYPTO_CXXFLAGS:-}" ]]; then
        printf '%s\n' "$GOXRPL_MPT_CRYPTO_CXXFLAGS"
        return
    fi

    case "$(uname -s)" in
        Darwin)
            printf '%s\n' '-stdlib=libc++'
            ;;
        Linux)
            if [[ "$(profile_libcxx)" == "libc++" ]]; then
                printf '%s\n' '-stdlib=libc++'
            fi
            ;;
        *)
            die "unsupported host OS $(uname -s); set GOXRPL_MPT_CRYPTO_CXXFLAGS"
            ;;
    esac
}

append_flag_string() {
    local existing="$1"
    local addition="$2"
    if [[ -z "$addition" ]]; then
        printf '%s\n' "$existing"
        return
    fi
    local -a addition_flags=()
    read -r -a addition_flags <<< "$addition"
    local flag

    for flag in "${addition_flags[@]}"; do
        case " $existing " in
            *" $flag "*) ;;
            *) existing="${existing:+$existing }$flag" ;;
        esac
    done
    printf '%s\n' "$existing"
}

prepend_path() {
    local prefix="$1"
    local existing="${2:-}"
    case ":$existing:" in
        *":$prefix:"*) printf '%s\n' "$existing" ;;
        *) printf '%s\n' "$prefix${existing:+:$existing}" ;;
    esac
}

print_env() {
    require_command pkg-config
    [[ -f "$output_dir/mpt-crypto.pc" ]] || die \
        "mpt-crypto is not set up; run 'just setup-mpt-crypto' first"

    local pkg_config_path
    pkg_config_path="$(prepend_path "$output_dir" "${PKG_CONFIG_PATH:-}")"
    local runtime_flags
    runtime_flags="$(cxx_runtime_flags)"
    local cgo_ldflags
    cgo_ldflags="$(append_flag_string "${CGO_LDFLAGS:-}" "$runtime_flags")"
    local cgo_cxxflags
    cgo_cxxflags="$(append_flag_string "${CGO_CXXFLAGS:-}" "$(cxx_compile_flags)")"

    # %q makes this output safe for `eval "$(just mpt-crypto-env)"`.
    printf 'export CONAN_HOME=%q\n' "$conan_home"
    printf 'export PKG_CONFIG_PATH=%q\n' "$pkg_config_path"
    printf 'export GOXRPL_MPT_CRYPTO_CXX_RUNTIME=%q\n' "$runtime_flags"
    printf 'export CGO_LDFLAGS=%q\n' "$cgo_ldflags"
    printf 'export CGO_CXXFLAGS=%q\n' "$cgo_cxxflags"
}

check_pkg_config() {
    require_command pkg-config
    local pkg_config_path
    pkg_config_path="$(prepend_path "$output_dir" "${PKG_CONFIG_PATH:-}")"
    PKG_CONFIG_PATH="$pkg_config_path" pkg-config --exists mpt-crypto || die \
        "pkg-config cannot resolve mpt-crypto; evaluate 'just mpt-crypto-env'"
    PKG_CONFIG_PATH="$pkg_config_path" pkg-config --exists goxrpl-mpt-crypto || die \
        "pkg-config cannot resolve the goXRPL mpt-crypto metadata"
    PKG_CONFIG_PATH="$pkg_config_path" pkg-config --exists libsecp256k1 || die \
        "pkg-config cannot resolve the Conan libsecp256k1 alias"
    PKG_CONFIG_PATH="$pkg_config_path" pkg-config --validate mpt-crypto
    PKG_CONFIG_PATH="$pkg_config_path" pkg-config --validate goxrpl-mpt-crypto
    PKG_CONFIG_PATH="$pkg_config_path" pkg-config --validate libsecp256k1

    local conan_mpt_prefix project_mpt_prefix conan_mpt_libdir project_mpt_libdir
    conan_mpt_prefix="$(PKG_CONFIG_PATH="$pkg_config_path" pkg-config --variable=prefix mpt-crypto)"
    project_mpt_prefix="$(PKG_CONFIG_PATH="$pkg_config_path" pkg-config --variable=prefix goxrpl-mpt-crypto)"
    conan_mpt_libdir="$(PKG_CONFIG_PATH="$pkg_config_path" pkg-config --variable=libdir mpt-crypto)"
    project_mpt_libdir="$(PKG_CONFIG_PATH="$pkg_config_path" pkg-config --variable=libdir goxrpl-mpt-crypto)"
    [[ "$project_mpt_prefix" == "$conan_mpt_prefix" && "$project_mpt_libdir" == "$conan_mpt_libdir" ]] || die \
        "goXRPL mpt-crypto metadata resolves outside the Conan package"

    local conan_secp_prefix legacy_secp_prefix conan_secp_libdir legacy_secp_libdir
    conan_secp_prefix="$(PKG_CONFIG_PATH="$pkg_config_path" pkg-config --variable=prefix secp256k1)"
    legacy_secp_prefix="$(PKG_CONFIG_PATH="$pkg_config_path" pkg-config --variable=prefix libsecp256k1)"
    conan_secp_libdir="$(PKG_CONFIG_PATH="$pkg_config_path" pkg-config --variable=libdir secp256k1)"
    legacy_secp_libdir="$(PKG_CONFIG_PATH="$pkg_config_path" pkg-config --variable=libdir libsecp256k1)"
    [[ "$legacy_secp_prefix" == "$conan_secp_prefix" ]] || die \
        "libsecp256k1 resolves outside the Conan package prefix"
    [[ "$legacy_secp_libdir" == "$conan_secp_libdir" ]] || die \
        "libsecp256k1 resolves outside the Conan package library directory"
    [[ "$conan_secp_prefix" == "$conan_home"/* ]] || die \
        "secp256k1 resolves outside the project-local Conan cache"

    printf 'mpt-crypto %s\n' "$(PKG_CONFIG_PATH="$pkg_config_path" pkg-config --modversion mpt-crypto)"
    printf 'libsecp256k1 %s (%s)\n' \
        "$(PKG_CONFIG_PATH="$pkg_config_path" pkg-config --modversion libsecp256k1)" \
        "$legacy_secp_libdir"
    printf 'pkg-config cflags: %s\n' "$(PKG_CONFIG_PATH="$pkg_config_path" pkg-config --cflags mpt-crypto)"
    printf 'pkg-config static libs: %s\n' \
        "$(PKG_CONFIG_PATH="$pkg_config_path" pkg-config --libs --static mpt-crypto)"
}

check_static_link() {
    local pkg_config_path
    pkg_config_path="$(prepend_path "$output_dir" "${PKG_CONFIG_PATH:-}")"
    local runtime_flags
    runtime_flags="$(cxx_runtime_flags)"
    local cgo_ldflags
    cgo_ldflags="$(append_flag_string "${CGO_LDFLAGS:-}" "$runtime_flags")"
    local cgo_cxxflags
    cgo_cxxflags="$(append_flag_string "${CGO_CXXFLAGS:-}" "$(cxx_compile_flags)")"
    PKG_CONFIG_PATH="$pkg_config_path" \
        CGO_LDFLAGS="$cgo_ldflags" \
        CGO_CXXFLAGS="$cgo_cxxflags" \
        go test -tags mptcrypto -run '^$' ./crypto/mptcrypto >/dev/null
    printf '%s\n' 'mpt-crypto static link: ok'
}

run_upstream_tests() {
    require_command ctest

    local package_ref
    package_ref="$(
        CONAN_HOME="$conan_home" conan list 'mpt-crypto/1.0.2:*' \
            --filter-options '&:tests=True' \
            --format=compact |
            awk '$1 ~ /^mpt-crypto\/1\.0\.2#[^%[:space:]]+:[0-9a-f]+$/ { print $1; exit }'
    )"
    [[ -n "$package_ref" ]] || die "could not locate the tested mpt-crypto package"

    local build_dir
    build_dir="$(CONAN_HOME="$conan_home" conan cache path "$package_ref" --folder=build)"
    [[ -f "$build_dir/build/Release/CTestTestfile.cmake" ]] || die \
        "mpt-crypto test manifest is missing from $build_dir"

    ctest --test-dir "$build_dir/build/Release" --output-on-failure
}

run_tests() {
    local package="${1:-./...}"
    shift || true
    local test_tags="${GOXRPL_MPT_CRYPTO_TEST_TAGS:-mptcrypto}"
    local runtime_flags
    runtime_flags="$(cxx_runtime_flags)"
    local cgo_ldflags
    cgo_ldflags="$(append_flag_string "${CGO_LDFLAGS:-}" "$runtime_flags")"
    local cgo_cxxflags
    cgo_cxxflags="$(append_flag_string "${CGO_CXXFLAGS:-}" "$(cxx_compile_flags)")"
    local pkg_config_path
    pkg_config_path="$(prepend_path "$output_dir" "${PKG_CONFIG_PATH:-}")"

    check_pkg_config
    check_static_link
    PKG_CONFIG_PATH="$pkg_config_path" \
        CGO_LDFLAGS="$cgo_ldflags" \
        CGO_CXXFLAGS="$cgo_cxxflags" \
        go test -tags "$test_tags" "$package" "$@"
}

main() {
    local action="${1:-}"
    prepare_conan_home
    case "$action" in
        setup)
            install_graph
            ;;
        env)
            print_env
            ;;
        test)
            require_command go
            install_graph
            run_upstream_tests
            shift
            run_tests "$@"
            ;;
        *)
            usage
            return 2
            ;;
    esac
}

cd "$repo_root"
main "$@"
