# tests/test_wallet.py
import pytest
from polywin.wallet import Wallet

TEST_KEY = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
TEST_ADDR = "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"


def test_derives_correct_address():
    assert Wallet.from_private_key(TEST_KEY).address == TEST_ADDR


def test_address_is_checksummed():
    addr = Wallet.from_private_key(TEST_KEY).address
    assert addr != addr.lower() and addr != addr.upper()


def test_sign_returns_hex():
    sig = Wallet.from_private_key(TEST_KEY).sign_message("hello")
    assert sig.startswith("0x") and len(sig) == 132


def test_sign_is_deterministic():
    w = Wallet.from_private_key(TEST_KEY)
    assert w.sign_message("test") == w.sign_message("test")


def test_invalid_key_raises():
    with pytest.raises(ValueError):
        Wallet.from_private_key("bad-key")
