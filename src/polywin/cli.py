from __future__ import annotations
import asyncio
import typer
from rich.console import Console
from rich.table import Table

app = typer.Typer(name="polywin", help="Polymarket automated trading bot", add_completion=False)
console = Console()


@app.command()
def run(
    paper: bool = typer.Option(False, "--paper", help="Simulate orders locally, no real capital at risk"),
    config: str = typer.Option("config.yml", "--config", help="Path to config.yml"),
):
    """Start the trading engine."""
    from polywin.config import Settings, load_app_config
    from polywin.logger import setup_logging
    from polywin.engine import Engine
    from polywin.strategies.copy_trading import CopyTradingStrategy

    setup_logging()
    settings = Settings()
    app_config = load_app_config(config)

    engine = Engine(settings=settings, config=app_config, paper=paper)
    if app_config.strategies.copy_trading.enabled:
        engine.register(CopyTradingStrategy())

    mode = "[yellow bold]PAPER[/]" if paper else "[green bold]LIVE[/]"
    console.print(f"\n[bold]Polywin[/] starting — mode: {mode}\n")
    asyncio.run(engine.start())


@app.command()
def status():
    """Show open positions and today's trade count."""
    from datetime import datetime, timezone
    from polywin.config import Settings
    from polywin.db import get_positions, get_today_trades

    settings = Settings()

    async def _show() -> None:
        positions = await get_positions()
        date_str = datetime.now(timezone.utc).strftime("%Y-%m-%d")
        trades = await get_today_trades(date_str)
        live_trades = [t for t in trades if not t["paper"]]
        paper_trades = [t for t in trades if t["paper"]]

        t = Table(title=f"Open Positions ({len(positions)})")
        t.add_column("Token", style="cyan")
        t.add_column("Outcome")
        t.add_column("Size", justify="right")
        t.add_column("Avg Price", justify="right")
        for p in positions:
            t.add_row(p["token_id"][:14] + "…", p["outcome"], f"{p['size']:.2f}", f"{p['avg_price']:.3f}")
        console.print(t)
        console.print(
            f"\nToday ({date_str}): [green]{len(live_trades)} live[/]  [yellow]{len(paper_trades)} paper[/]\n"
        )

    asyncio.run(_show())


@app.command()
def markets(
    min_vol: float = typer.Option(10_000, "--min-vol", help="Minimum volume in USD"),
    limit: int = typer.Option(20, "--limit"),
):
    """List tradeable markets right now."""
    from polywin.config import Settings
    from polywin.clients.gamma import GammaClient

    s = Settings()
    with GammaClient(s.gamma_api_url) as gamma:
        items = gamma.get_tradeable_markets(min_volume=min_vol, limit=limit)

    t = Table(title=f"Tradeable Markets  (vol ≥ ${min_vol:,.0f})")
    t.add_column("Question", max_width=52)
    t.add_column("Yes / No", justify="right")
    t.add_column("Volume", justify="right")
    for m in items:
        prices = " / ".join(f"{p:.2f}" for p in m.outcome_prices)
        t.add_row(m.question[:52], prices, f"${m.volume:,.0f}")
    console.print(t)


@app.command("top-traders")
def top_traders(
    min_pnl: float = typer.Option(10_000, "--min-pnl", help="Minimum all-time P&L in USD"),
    limit: int = typer.Option(20, "--limit"),
):
    """List top traders from the Polymarket leaderboard."""
    from polywin.config import Settings
    from polywin.clients.data_api import DataApiClient

    s = Settings()
    with DataApiClient(s.data_api_url) as data:
        traders = data.get_top_traders(min_pnl=min_pnl, limit=limit)

    t = Table(title=f"Top Traders  (P&L ≥ ${min_pnl:,.0f})")
    t.add_column("Name / Address", max_width=18)
    t.add_column("P&L", justify="right", style="green")
    t.add_column("Volume", justify="right")
    for tr in traders:
        label = tr.name if tr.name else tr.address[:14] + "…"
        t.add_row(label, f"${tr.pnl:,.0f}", f"${tr.volume:,.0f}")
    console.print(t)


@app.command("derive-creds")
def derive_creds():
    """Derive CLOB API credentials from your private key."""
    from polywin.config import Settings
    from polywin.clients.creds import derive_clob_creds

    s = Settings()
    console.print("Deriving CLOB credentials…")
    creds = derive_clob_creds(s.private_key, s.clob_host, s.chain_id)
    console.print("\nAdd these to your [bold].env[/bold] file:\n")
    console.print(f"[cyan]CLOB_API_KEY[/]={creds.api_key}")
    console.print(f"[cyan]CLOB_API_SECRET[/]={creds.api_secret}")
    console.print(f"[cyan]CLOB_API_PASSPHRASE[/]={creds.api_passphrase}\n")


if __name__ == "__main__":
    app()
