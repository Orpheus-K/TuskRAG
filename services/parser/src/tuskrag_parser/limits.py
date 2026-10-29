import json
from dataclasses import dataclass, fields, replace
from typing import Any


class InvalidLimits(Exception):
    pass


class LimitExceeded(Exception):
    pass


class InvalidDocument(Exception):
    pass


class UnsupportedDocument(Exception):
    pass


class EncryptedDocument(Exception):
    pass


@dataclass(frozen=True, slots=True)
class Limits:
    max_input_bytes: int = 200 * 1024 * 1024
    max_output_bytes: int = 200 * 1024 * 1024
    max_pages: int = 2_000
    max_rows: int = 100_000
    max_columns: int = 1_024
    max_cells: int = 1_000_000
    max_field_chars: int = 1_000_000
    max_blocks: int = 100_000
    max_archive_entries: int = 10_000
    max_uncompressed_bytes: int = 1024 * 1024 * 1024
    max_compression_ratio: int = 100
    max_xml_nodes: int = 2_000_000


def parse_limits(raw: str) -> Limits:
    if len(raw.encode("utf-8")) > 4096:
        raise InvalidLimits
    try:
        values: Any = json.loads(raw)
    except json.JSONDecodeError as error:
        raise InvalidLimits from error
    if not isinstance(values, dict):
        raise InvalidLimits
    defaults = Limits()
    bounds = {field.name: getattr(defaults, field.name) for field in fields(defaults)}
    if any(
        key not in bounds or type(value) is not int or value <= 0 or value > bounds[key]
        for key, value in values.items()
    ):
        raise InvalidLimits
    return replace(defaults, **values)
