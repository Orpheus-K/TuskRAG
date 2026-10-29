import stat
from pathlib import PurePosixPath
from typing import BinaryIO
from zipfile import ZipFile

import olefile
from defusedxml import ElementTree
from olefile.olefile import OleFileError

from tuskrag_parser.limits import (
    EncryptedDocument,
    InvalidDocument,
    LimitExceeded,
    Limits,
    UnsupportedDocument,
)

CONTENT_TYPES = "[Content_Types].xml"
CONTENT_NS = "{http://schemas.openxmlformats.org/package/2006/content-types}"
MAX_MANIFEST_BYTES = 4 * 1024 * 1024


def reject_compound_file(source: BinaryIO) -> None:
    if source.read(8) != olefile.MAGIC:
        source.seek(0)
        return
    source.seek(0)
    try:
        with olefile.OleFileIO(
            source, raise_defects=olefile.DEFECT_INCORRECT
        ) as compound:
            if (
                compound.exists("EncryptionInfo")
                and compound.exists("EncryptedPackage")
                and compound.get_type("EncryptionInfo") == olefile.STGTY_STREAM
                and compound.get_type("EncryptedPackage") == olefile.STGTY_STREAM
            ):
                raise EncryptedDocument
    except (OleFileError, ValueError, IndexError) as error:
        raise InvalidDocument from error
    raise UnsupportedDocument


def preflight(archive: ZipFile, limits: Limits) -> None:
    entries = archive.infolist()
    if len(entries) > limits.max_archive_entries:
        raise LimitExceeded
    names: set[str] = set()
    total = 0
    for info in entries:
        path = PurePosixPath(info.filename)
        if (
            not info.filename
            or "\\" in info.filename
            or path.is_absolute()
            or ".." in path.parts
            or info.filename.casefold() in names
            or stat.S_ISLNK(info.external_attr >> 16)
        ):
            raise InvalidDocument
        names.add(info.filename.casefold())
        if info.flag_bits & 1:
            raise EncryptedDocument
        total += info.file_size
        if total > limits.max_uncompressed_bytes or (
            info.file_size > 0
            and (
                info.compress_size == 0
                or info.file_size > info.compress_size * limits.max_compression_ratio
            )
        ):
            raise LimitExceeded
    if CONTENT_TYPES not in archive.NameToInfo:
        raise InvalidDocument
    manifest = archive.getinfo(CONTENT_TYPES)
    if manifest.file_size > MAX_MANIFEST_BYTES:
        raise LimitExceeded
    types = ElementTree.fromstring(archive.read(manifest), forbid_dtd=True)
    if types.tag != CONTENT_NS + "Types":
        raise InvalidDocument
    defaults: dict[str, str] = {}
    overrides: dict[str, str] = {}
    for item in types:
        if item.tag == CONTENT_NS + "Default":
            extension, content_type = item.get("Extension"), item.get("ContentType")
            if not extension or not content_type or extension.casefold() in defaults:
                raise InvalidDocument
            defaults[extension.casefold()] = content_type.casefold()
        elif item.tag == CONTENT_NS + "Override":
            name, content_type = item.get("PartName"), item.get("ContentType")
            if (
                not name
                or not name.startswith("/")
                or not content_type
                or name.casefold() in overrides
            ):
                raise InvalidDocument
            overrides[name.casefold()] = content_type.casefold()
        else:
            raise InvalidDocument
    if any(
        "macroenabled" in value or "vbaproject" in value
        for value in (*defaults.values(), *overrides.values())
    ):
        raise UnsupportedDocument
    nodes = 0
    for info in entries:
        if info.is_dir():
            continue
        name = info.filename.casefold()
        extension = name.rpartition(".")[2]
        content_type = overrides.get("/" + name, defaults.get(extension, ""))
        if name.endswith((".xml", ".rels")) or content_type.endswith(("+xml", "/xml")):
            with archive.open(info) as xml:
                for _, element in ElementTree.iterparse(
                    xml, events=("end",), forbid_dtd=True
                ):
                    nodes += 1
                    if nodes > limits.max_xml_nodes:
                        raise LimitExceeded
                    element.clear()
