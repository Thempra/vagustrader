from dataclasses import dataclass
from py_clob_client.client import ClobClient as _ClobClient


@dataclass
class ClobCreds:
    api_key: str
    api_secret: str
    api_passphrase: str


def derive_clob_creds(
    private_key: str,
    host: str = "https://clob.polymarket.com",
    chain_id: int = 137,
) -> ClobCreds:
    """Derive CLOB API credentials deterministically from private key (no on-chain tx)."""
    client = _ClobClient(host=host, key=private_key, chain_id=chain_id)
    raw = client.derive_api_key()
    return ClobCreds(
        api_key=raw.api_key,
        api_secret=raw.api_secret,
        api_passphrase=raw.api_passphrase,
    )
