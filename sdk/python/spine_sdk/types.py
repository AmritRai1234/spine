"""Type definitions for the Spine Python SDK."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Callable, Dict, List, Optional, TypeVar

T = TypeVar("T")


@dataclass
class SpineClientOptions:
    """Configuration for initialising a SpineClient."""

    base_url: str
    api_key: Optional[str] = None
    ws_url: Optional[str] = None
    auto_reconnect: bool = True
    reconnect_interval_s: float = 3.0


@dataclass
class EmitResponse:
    """Response from a POST /emit call."""

    status: str
    event: str
    routes_matched: int = 0
    emitted_states: List[str] = field(default_factory=list)
    result: Optional[Any] = None
    error: Optional[str] = None

    @classmethod
    def from_dict(cls, data: Dict[str, Any]) -> "EmitResponse":
        return cls(
            status=data.get("status", ""),
            event=data.get("event", ""),
            routes_matched=data.get("routes_matched", 0),
            emitted_states=data.get("emitted_states", []),
            result=data.get("result"),
            error=data.get("error"),
        )


@dataclass
class QueryOptions:
    """Options for querying table rows."""

    limit: Optional[int] = None
    offset: Optional[int] = None
    cursor: Optional[int] = None
    where: Optional[Dict[str, str]] = None


@dataclass
class TableQueryResponse:
    """Response from a GET /tables/{name} call."""

    status: str
    table: str
    count: int = 0
    rows: List[Dict[str, Any]] = field(default_factory=list)
    next_cursor: Optional[int] = None
    error: Optional[str] = None

    @classmethod
    def from_dict(cls, data: Dict[str, Any]) -> "TableQueryResponse":
        return cls(
            status=data.get("status", ""),
            table=data.get("table", ""),
            count=data.get("count", 0),
            rows=data.get("rows", []),
            next_cursor=data.get("next_cursor"),
            error=data.get("error"),
        )


@dataclass
class TableInfo:
    """Summary of a database table."""

    name: str
    rows: int = 0


@dataclass
class EventLog:
    """A single event audit log entry."""

    id: int
    event: str
    payload: Dict[str, Any] = field(default_factory=dict)
    emitted_states: List[str] = field(default_factory=list)
    created_at: str = ""


# Callback type for real-time state subscriptions.
StateCallback = Callable[[str, Dict[str, Any]], None]
