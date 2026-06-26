# tests/clients/test_creds.py
from unittest.mock import MagicMock, patch
from polywin.clients.creds import derive_clob_creds, ClobCreds


def test_returns_typed_creds():
    mock_raw = MagicMock(api_key="k", api_secret="s", api_passphrase="p")
    mock_client = MagicMock()
    mock_client.derive_api_key.return_value = mock_raw

    with patch("polywin.clients.creds._ClobClient", return_value=mock_client):
        creds = derive_clob_creds("0x" + "a" * 64)

    assert isinstance(creds, ClobCreds)
    assert creds.api_key == "k"
    assert creds.api_secret == "s"
    assert creds.api_passphrase == "p"
    mock_client.derive_api_key.assert_called_once()
