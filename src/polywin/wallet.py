from eth_account import Account
from eth_account.messages import encode_defunct


class Wallet:
    def __init__(self, private_key: str):
        account = Account.from_key(private_key)
        self._address: str = account.address
        self._sign_fn = account.sign_message
        del account

    @property
    def address(self) -> str:
        return self._address

    def sign_message(self, message: str) -> str:
        signed = self._sign_fn(encode_defunct(text=message))
        # HexBytes.hex() returns signature without '0x' prefix, so we prepend it
        return "0x" + signed.signature.hex()

    @classmethod
    def from_private_key(cls, key: str) -> "Wallet":
        return cls(key)
