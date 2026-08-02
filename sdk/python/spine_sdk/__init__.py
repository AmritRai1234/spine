"""
Spine Python SDK — Official client for Spine event-driven backends.

Usage:
    from spine_sdk import SpineClient

    client = SpineClient(base_url="http://localhost:8080", api_key="your-key")
    result = client.emit("SUBMIT_LEAD", {"email": "jane@example.com", "name": "Jane"})
"""

from spine_sdk.client import SpineClient
from spine_sdk.types import (
    EmitResponse,
    QueryOptions,
    SpineClientOptions,
    TableQueryResponse,
)

__all__ = [
    "SpineClient",
    "SpineClientOptions",
    "EmitResponse",
    "QueryOptions",
    "TableQueryResponse",
]

__version__ = "1.0.0"
