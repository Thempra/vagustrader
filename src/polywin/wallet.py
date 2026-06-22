from eth_account import Account
from eth_account.messages import encode_defunct


class Wallet:
    def __init__(self, private_key: str):
        self._account = Account.from_key(private_key)

    @property
    def address(self) -> str:
        return self._account.address

    def sign_message(self, message: str) -> str:
        signed = self._account.sign_message(encode_defunct(text=message))
        return "0x" + signed.signature.hex()

    @classmethod
    def from_private_key(cls, key: str) -> "Wallet":
        return cls(key)
