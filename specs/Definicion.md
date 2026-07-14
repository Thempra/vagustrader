Proyecto de trading automatizado en Polymarket para generar ingresos. Polymarket es una plataforma de mercados de predicción (prediction markets) basada en blockchain (principalmente Polygon), donde se apuesta en eventos reales (política, crypto, deportes, etc.) usando USDC. Los bots dominan actualmente: muchos de los top traders en los leaderboards son automatizados, con estrategias de arbitraje, copy-trading y AI que explotan latencia, desajustes de precios y datos externos.
Advertencia importante: El trading implica alto riesgo de pérdida de capital. Muchos bots fallan por ejecución pobre (fills incompletos, ghost trades, fees), volatilidad y cambios en la plataforma (fees dinámicos, vigilancia anti-insider). Polymarket ha aumentado escrutinio por casos de insider trading (ej. apuestas con info clasificada). Usa solo capital que puedas perder, prueba en paper trading y cumple regulaciones locales (restringido en algunos países como Brasil).
Especificaciones del Proyecto Recomendado
Un sistema híbrido (no solo un bot simple) combina varias estrategias para diversificar y mitigar riesgos. Objetivo: 5-15% mensual neto en condiciones ideales (según reportes de bots exitosos), pero realista es variable y con drawdowns.
Componentes principales:

Backend/Core: Node.js/TypeScript o Python (con py-clob-client). Conecta a Polymarket APIs:
Gamma API (gamma-api.polymarket.com): Datos de mercados, eventos, precios.
Data API (data-api.polymarket.com): Posiciones, trades, leaderboards.
CLOB API (Central Limit Order Book): Para órdenes en tiempo real, orderbook depth.

Estrategias clave (implementar en paralelo):
Copy Trading: Replica wallets top del leaderboard (filtra por profit consistente, win rate >70%, volumen moderado, no solo suerte). Ejemplos de wallets rentables históricos: busca en leaderboard actual (Countryside, bossoskil1, etc.).
Arbitraje/Latencia: En mercados crypto cortos (BTC/ETH/SOL up/down en 5-15 min). Compra en Polymarket cuando laggea vs. Binance/Coinbase spot. Bots han convertido $313 → $414k.
AI Probability Arbitrage: Modelos ensemble (news + social + polling) para detectar mispricings. Win rates reportados 65-85%.
Market Making / Liquidity Provision: Provee liquidez para rebates (programa de makers).
Opcional: Sports/weather bots (explotan lags en eventos en vivo), correlation trading.

Infraestructura:
VPS low-latency (cerca de servidores Polymarket o exchanges): QuantVPS u similares. Sub-100ms crítico para arbitraje.
Monitoreo: Alertas Telegram/Discord, logging de fills on-chain (importante, hay discrepancias API vs. blockchain).
Risk Management: Position sizing (1-2% por trade), stop-loss, max drawdown, diversificación (no >20-30% en una categoría). Kelly criterion o similar para sizing.
Wallet: Deposit wallet (nueva recomendación Polymarket).

Frontend/Dashboard: Web simple (React) o Telegram bot para control, stats P&L, alerts.
Seguridad: Claves API seguras, no hardcodear, monitoreo de anomalías. Evita insider info.

Costo estimado inicial: VPS $20-100/mes + fees Polymarket (taker ~0.5-2% según categoría) + desarrollo (o usar open-source como echandsome/Polymarket-betting-bot en GitHub).
Bots Existentes con Mejor Rendimiento (2026)
Los copy-trading y sniper bots son populares para principiantes. Top mencionados:

Polycop (@polycop_bot en Telegram): Rápido, barato, bueno para mirroring traders con >70% win rate. Destaca en velocidad de copy. Recomendado para empezar.
Polygun: Preciso para sniping high-probability setups. Usuarios reportan buenos retornos pasivos.
Polycool / PredictEngine / Polystrat: AI-driven, hands-off, news-based.
Otros: Fliprbot, PolyClaw, NautilusTrader (para custom), PolyBro (news).

Rendimiento: Bots top han generado millones (ej. $2.2M en 2 meses con AI, $8M en sports por lag). Copy-trading sigue whales consistentes. Muchos reportan 70-98% win rate en nichos, pero ejecución real importa (un bot con 68% win rate perdía dinero por problemas de fills).
Prueba varios (muchos tienen trials) y verifica en leaderboard. Open-source para customizar.
Parámetros y Fuentes de Información
Parámetros clave para el bot:

Umbrales: Edge mínimo (probabilidad modelo vs. market price, ej. >5-10% mispricing). Win probability >60-65%.
Tamaño: 0.5-2% bankroll por trade. Ajuste dinámico por liquidez/volatilidad.
Tiempo: Monitoreo 24/7, pero foco en apertura/cierre mercados o eventos en vivo.
Fees: Calcular net (taker + gas). Categorías: Sports más baratas.
Filtros: Volumen >$X, liquidez suficiente, evitar mercados ambiguos.
Salidas: Automático al resolver o cuando edge desaparece.

Fuentes de datos (integrar vía API/scraping):

Polymarket nativo: Orderbook, precios, volúmenes.
Noticias/Sentiment: X/Twitter (x_keyword_search o APIs), news aggregators (Glint, Kreo), RSS. AI (LLMs como Grok/Claude) para análisis rápido.
Polling: FiveThirtyEight, RealClearPolitics, agregadores locales (para política).
Crypto/Sports: Binance/Coinbase websockets (para latencia arb), odds deportivas sharp books.
On-chain/Social: Dune Analytics, wallet trackers, leaderboards Polymarket.
Otros: Weather APIs (para nichos), economic indicators, flight trackers (geopolítica).

Usa ensemble models: combina múltiples fuentes y pondera por confiabilidad histórica.
Pasos para Implementar

Estudia docs.polymarket.com y prueba API pública.
Desarrolla o deploya bot open-source + integra estrategias.
Backtest exhaustivo (paper trading 1-2 meses).
Deploy en VPS, monitorea y optimiza.
Escala gradualmente.

Para ingresos sostenibles, enfócate en ejecución impecable + edge de datos más que predicciones puras. Los humanos luchan vs. bots; un sistema bien hecho puede competir. Si necesitas código base o más detalles específicos, dame más info sobre tu stack/preferencia (copy vs. custom). ¡Éxito y trade responsibly!
