#!/usr/bin/env python3
"""
Generate synthetic fine-tuning dataset for Gemma4 tool calling.
Focuses on news analysis + finance trading workflows.
"""

import json
import random
from typing import List, Dict

# Common symbols and entities for varied examples
TECH_SYMBOLS = ["NVDA", "AMD", "INTC", "AAPL", "MSFT", "GOOGL", "META", "TSLA"]
FINANCE_SYMBOLS = ["JPM", "GS", "BAC", "WFC", "C"]
CRYPTO_SYMBOLS = ["BTC", "ETH", "SOL", "AVAX"]
ENERGY_SYMBOLS = ["XOM", "CVX", "COP", "SLB"]
PHARMA_SYMBOLS = ["PFE", "JNJ", "MRNA", "LLY"]

ENTITIES = [
    "Apple Inc.", "Microsoft", "NVIDIA", "Tesla", "Amazon", "Google",
    "Federal Reserve", "JPMorgan", "Goldman Sachs", "Warren Buffett",
    "BlackRock", "Vanguard", "European Central Bank"
]

SAMPLES_PER_CATEGORY = 100

def create_example(instruction: str, output: str) -> Dict:
    """Create a single training example."""
    return {
        "instruction": instruction,
        "input": "",
        "output": output
    }

def generate_category1_examples() -> List[Dict]:
    """Category 1: News Discovery → Trading Strategy (30 examples)"""
    examples = []
    
    # Example 1: Simple entity news check + buy
    examples.append(create_example(
        "Check recent news about NVIDIA and buy 10 shares if it's positive",
        """I'll research NVIDIA news and make a trading decision. Here's my approach:

1. Find recent NVIDIA news articles
   Tool: query_articles_by_entity
   Parameters:
     - entity: "NVIDIA"
     - start_date: "2026-06-15T00:00:00Z"
     - end_date: "2026-06-29T23:59:59Z"
     - limit: 20

2. List available trading accounts
   Tool: oms_list_accounts

3. If news is positive, place buy order
   Tool: oms_place_order
   Parameters:
     - account_id: [from step 2]
     - asset_symbol: "NVDA"
     - asset_type: "stock"
     - side: "buy"
     - order_type: "market"
     - quantity: 10

After analyzing the news sentiment and recent developments, I'll execute the trade if indicators are favorable."""
    ))
    
    # Example 2: Multiple entity comparison
    examples.append(create_example(
        "Compare news sentiment for Tesla vs traditional auto makers, then invest in the better one",
        """I'll compare news coverage and make an investment decision:

1. Get Tesla news
   Tool: query_articles_by_entity
   Parameters:
     - entity: "Tesla"
     - start_date: "2026-06-01T00:00:00Z"
     - limit: 30

2. Find articles about traditional automakers
   Tool: similar_articles_to_subject
   Parameters:
     - subject: "traditional automotive manufacturers Ford GM Toyota"
     - limit: 30
     - section: "finance"

3. Get my trading accounts
   Tool: oms_list_accounts

4. Check current account balance
   Tool: oms_get_balances
   Parameters:
     - account_id: [from step 3]

5. Place order for winner
   Tool: oms_place_order
   Parameters:
     - account_id: [from step 3]
     - asset_symbol: [based on analysis]
     - asset_type: "stock"
     - side: "buy"
     - order_type: "market"
     - quantity: [calculated based on available balance]

Based on the comparative news analysis, I'll invest in whichever company shows stronger growth indicators and positive sentiment."""
    ))
    
    # Example 3: Sector news → multiple buys
    examples.append(create_example(
        "Find the most mentioned AI companies in today's news and buy top 3",
        """I'll identify trending AI companies and build positions:

1. Get today's tech news
   Tool: finance_todays_news_titles
   Parameters:
     - category: "technology"
     - limit: 50

2. Discover AI-related entities
   Tool: discover_entities_by_search
   Parameters:
     - search_text: "artificial intelligence"
     - prefix_only: false
     - limit: 20

3. Find similar AI companies
   Tool: similar_entities
   Parameters:
     - entity: "artificial intelligence"
     - limit: 15

4. Check my account
   Tool: oms_get_account_summary
   Parameters:
     - account_id: [account id]

5. Buy top 3 companies (assuming NVDA, MSFT, GOOGL)
   Tool: oms_place_order
   Parameters:
     - account_id: [account id]
     - asset_symbol: "NVDA"
     - asset_type: "stock"
     - side: "buy"
     - order_type: "market"
     - quantity: 5

   [Repeat for MSFT and GOOGL]

This strategy captures the AI momentum by investing in the most newsworthy companies."""
    ))
    
    # Example 4: News alerts → quick trade
    examples.append(create_example(
        "Check if there's breaking news about Apple, and if so, buy 15 shares immediately",
        """I'll check for recent Apple news and act quickly:

1. Get today's news
   Tool: todays_news_titles
   Parameters:
     - limit: 100

2. Query Apple-specific articles
   Tool: query_articles_by_entity
   Parameters:
     - entity: "Apple Inc."
     - start_date: "2026-06-29T00:00:00Z"
     - limit: 10

3. List my accounts
   Tool: oms_list_accounts

4. Place immediate market order
   Tool: oms_place_order
   Parameters:
     - account_id: [from step 3]
     - asset_symbol: "AAPL"
     - asset_type: "stock"
     - side: "buy"
     - order_type: "market"
     - quantity: 15

If breaking news is detected, the market order ensures immediate execution at current prices."""
    ))
    
    # Example 5: Crypto news → trade
    examples.append(create_example(
        "Search for Bitcoin regulation news and buy BTC if regulations are favorable",
        """I'll research Bitcoin regulatory developments:

1. Find Bitcoin regulation articles
   Tool: similar_articles_to_subject
   Parameters:
     - subject: "Bitcoin regulation cryptocurrency legal framework"
     - limit: 20
     - section: "finance"
     - sort_by_relevance: true

2. Check entity news
   Tool: query_articles_by_entity
   Parameters:
     - entity: "Bitcoin"
     - start_date: "2026-06-15T00:00:00Z"
     - limit: 30

3. Get account info
   Tool: oms_get_balances
   Parameters:
     - account_id: [account id]

4. If regulations favor crypto, buy BTC
   Tool: oms_place_order
   Parameters:
     - account_id: [account id]
     - asset_symbol: "BTC"
     - asset_type: "crypto"
     - side: "buy"
     - order_type: "market"
     - quantity: 0.5

Based on regulatory sentiment analysis, I'll proceed with the investment if the legal environment is supportive."""
    ))
    
    # Example 6-10: Additional variations with different patterns
    examples.append(create_example(
        "Monitor Federal Reserve news and buy financial stocks if interest rates are dropping",
        """I'll track monetary policy news and position accordingly:

1. Search for Federal Reserve news
   Tool: query_articles_by_entity
   Parameters:
     - entity: "Federal Reserve"
     - start_date: "2026-06-20T00:00:00Z"
     - limit: 25

2. Find interest rate articles
   Tool: similar_articles_to_subject
   Parameters:
     - subject: "interest rates monetary policy central bank"
     - limit: 20
     - section: "finance"

3. List my accounts
   Tool: oms_list_accounts

4. Buy financial sector stocks
   Tool: oms_place_order
   Parameters:
     - account_id: [from step 3]
     - asset_symbol: "JPM"
     - asset_type: "stock"
     - side: "buy"
     - order_type: "market"
     - quantity: 8

5. Buy another financial stock
   Tool: oms_place_order
   Parameters:
     - account_id: [from step 3]
     - asset_symbol: "GS"
     - asset_type: "stock"
     - side: "buy"
     - order_type: "market"
     - quantity: 5

If Fed signals rate cuts, financial stocks typically benefit from improved lending conditions."""
    ))
    
    examples.append(create_example(
        "Find pharma companies in the news and buy the most promising one",
        """I'll research pharmaceutical sector and identify opportunities:

1. Discover pharmaceutical entities
   Tool: discover_entities_by_search
   Parameters:
     - search_text: "pharmaceutical"
     - prefix_only: false
     - limit: 15

2. Check pharma news
   Tool: similar_articles_to_subject
   Parameters:
     - subject: "pharmaceutical drug approval clinical trials"
     - limit: 25
     - section: "finance"

3. Query specific company news
   Tool: query_articles_by_entity
   Parameters:
     - entity: "Pfizer"
     - start_date: "2026-06-01T00:00:00Z"
     - limit: 20

4. Get account summary
   Tool: oms_get_account_summary
   Parameters:
     - account_id: [account id]

5. Place buy order
   Tool: oms_place_order
   Parameters:
     - account_id: [account id]
     - asset_symbol: "PFE"
     - asset_type: "stock"
     - side: "buy"
     - order_type: "limit"
     - quantity: 20
     - limit_price: 28.50

Using a limit order protects against paying too much if the stock spikes."""
    ))
    
    examples.append(create_example(
        "Check energy sector news and buy oil stocks if prices are rising",
        """I'll analyze energy market news:

1. Get energy sector news
   Tool: similar_articles_to_subject
   Parameters:
     - subject: "oil prices energy sector crude petroleum"
     - limit: 30
     - section: "finance"

2. Check specific companies
   Tool: query_articles_by_entity
   Parameters:
     - entity: "ExxonMobil"
     - start_date: "2026-06-15T00:00:00Z"
     - limit: 15

3. Visualize oil stock performance
   Tool: finance_get_symbol_plot
   Parameters:
     - symbols: ["XOM", "CVX", "COP"]
     - start_date: "2026-04-01"
     - end_date: "2026-06-29"
     - asset_type: "stock"

4. Get my balance
   Tool: oms_get_balances
   Parameters:
     - account_id: [account id]

5. Buy energy stocks
   Tool: oms_place_order
   Parameters:
     - account_id: [account id]
     - asset_symbol: "XOM"
     - asset_type: "stock"
     - side: "buy"
     - order_type: "market"
     - quantity: 12

If oil prices show upward momentum, energy stocks typically follow."""
    ))
    
    # Add 22 more examples with variations...
    for i in range(SAMPLES_PER_CATEGORY):
        symbol = random.choice(TECH_SYMBOLS + FINANCE_SYMBOLS)
        entity = random.choice(ENTITIES)
        quantity = random.randint(5, 25)
        
        examples.append(create_example(
            f"Look for news about {entity} and buy {quantity} shares if momentum is positive",
            f"""I'll research {entity} and make a data-driven decision:

1. Query recent news
   Tool: query_articles_by_entity
   Parameters:
     - entity: "{entity}"
     - start_date: "2026-06-15T00:00:00Z"
     - limit: 20

2. Check my accounts
   Tool: oms_list_accounts

3. Place order if positive
   Tool: oms_place_order
   Parameters:
     - account_id: [from step 2]
     - asset_symbol: "{symbol}"
     - asset_type: "stock"
     - side: "buy"
     - order_type: "market"
     - quantity: {quantity}

Based on news sentiment and momentum indicators, I'll execute the trade."""
        ))
    
    return examples


def generate_category2_examples() -> List[Dict]:
    """Category 2: Portfolio Setup + News Monitoring (25 examples)"""
    examples = []
    
    # Example 1: Create tech portfolio
    examples.append(create_example(
        "Create a technology portfolio with equal allocations to NVIDIA, AMD, and Intel, then track their news",
        """I'll set up a tech-focused portfolio with news monitoring:

1. Create the portfolio
   Tool: pms_create_portfolio
   Parameters:
     - name: "Tech Chip Leaders"
     - description: "Portfolio focused on semiconductor industry leaders"
     - target_allocations: {"NVDA": 0.33, "AMD": 0.33, "INTC": 0.34}

2. Create trading account
   Tool: oms_create_account
   Parameters:
     - name: "Tech Trading Account"
     - initial_balance: 50000.00

3. Link account to portfolio
   Tool: pms_link_account
   Parameters:
     - portfolio_id: [from step 1]
     - account_id: [from step 2]
     - weight: 1.0

4. Set up news monitoring for NVIDIA
   Tool: query_articles_by_entity
   Parameters:
     - entity: "NVIDIA"
     - start_date: "2026-06-01T00:00:00Z"
     - limit: 20

5. Monitor AMD news
   Tool: query_articles_by_entity
   Parameters:
     - entity: "AMD"
     - start_date: "2026-06-01T00:00:00Z"
     - limit: 20

6. Track Intel news
   Tool: query_articles_by_entity
   Parameters:
     - entity: "Intel"
     - start_date: "2026-06-01T00:00:00Z"
     - limit: 20

This portfolio structure enables systematic tracking of all holdings with balanced allocations."""
    ))
    
    # Example 2: Growth portfolio
    examples.append(create_example(
        "Set up a growth portfolio focused on AI companies with 60% aggressive allocation",
        """I'll create an aggressive AI growth portfolio:

1. Discover AI entities
   Tool: discover_entities_by_search
   Parameters:
     - search_text: "artificial intelligence machine learning"
     - prefix_only: false
     - limit: 20

2. Create portfolio with aggressive allocation
   Tool: pms_create_portfolio
   Parameters:
     - name: "AI Growth Portfolio"
     - description: "Aggressive growth portfolio focusing on AI leaders"
     - target_allocations: {"NVDA": 0.40, "MSFT": 0.30, "GOOGL": 0.30}

3. Create account with substantial capital
   Tool: oms_create_account
   Parameters:
     - name: "AI Growth Account"
     - initial_balance: 100000.00

4. Link account
   Tool: pms_link_account
   Parameters:
     - portfolio_id: [from step 2]
     - account_id: [from step 3]

5. Monitor AI sector news
   Tool: similar_articles_to_subject
   Parameters:
     - subject: "artificial intelligence AI technology breakthrough"
     - limit: 30
     - section: "tech"

The aggressive allocation reflects high-growth potential in the AI sector."""
    ))
    
    # Add 23 more portfolio setup examples...
    portfolio_themes = [
        ("Dividend Income", "blue-chip dividend stocks", {"JPM": 0.25, "JNJ": 0.25, "PFE": 0.25, "XOM": 0.25}),
        ("Crypto Diversified", "diversified cryptocurrency holdings", {"BTC": 0.50, "ETH": 0.30, "SOL": 0.20}),
        ("ESG Sustainable", "environmentally sustainable companies", {"TSLA": 0.40, "AAPL": 0.30, "MSFT": 0.30}),
        ("Value Investing", "undervalued stocks", {"BAC": 0.35, "WFC": 0.35, "C": 0.30}),
        ("Tech Giants", "major technology companies", {"AAPL": 0.30, "MSFT": 0.30, "GOOGL": 0.40}),
    ]
    
    for theme_name, theme_desc, allocations in portfolio_themes:
        examples.append(create_example(
            f"Create a {theme_name} portfolio and monitor relevant news",
            f"""I'll establish a {theme_name} portfolio with news tracking:

1. Create portfolio
   Tool: pms_create_portfolio
   Parameters:
     - name: "{theme_name} Portfolio"
     - description: "Portfolio focused on {theme_desc}"
     - target_allocations: {json.dumps(allocations)}

2. Create trading account
   Tool: oms_create_account
   Parameters:
     - name: "{theme_name} Account"
     - initial_balance: {random.randint(30000, 100000)}.00

3. Link account to portfolio
   Tool: pms_link_account
   Parameters:
     - portfolio_id: [from step 1]
     - account_id: [from step 2]

4. Monitor sector news
   Tool: similar_articles_to_subject
   Parameters:
     - subject: "{theme_desc}"
     - limit: 25
     - section: "finance"

This portfolio aligns with the {theme_desc} investment thesis."""
        ))
    
    # Fill remaining examples with variations
    for i in range(SAMPLES_PER_CATEGORY):
        symbols = random.sample(TECH_SYMBOLS + FINANCE_SYMBOLS, 3)
        examples.append(create_example(
            f"Build a diversified portfolio tracking {', '.join(symbols)} with news alerts",
            f"""I'll create a diversified portfolio with monitoring:

1. Create portfolio
   Tool: pms_create_portfolio
   Parameters:
     - name: "Diversified Portfolio {i+1}"
     - description: "Multi-sector diversified holdings"
     - target_allocations: {{"{symbols[0]}": 0.40, "{symbols[1]}": 0.35, "{symbols[2]}": 0.25}}

2. Create account
   Tool: oms_create_account
   Parameters:
     - name: "Trading Account {i+1}"
     - initial_balance: {random.randint(40000, 80000)}.00

3. Link account
   Tool: pms_link_account
   Parameters:
     - portfolio_id: [from step 1]
     - account_id: [from step 2]

4. Monitor holdings
   Tool: query_articles_by_entity
   Parameters:
     - entity: "{symbols[0]}"
     - start_date: "2026-06-15T00:00:00Z"
     - limit: 15

Portfolio provides balanced exposure across sectors."""
        ))
    
    return examples


def generate_category3_examples() -> List[Dict]:
    """Category 3: News-Based Rebalancing (20 examples)"""
    examples = []
    
    # Example 1: Sector rotation based on news
    examples.append(create_example(
        "Check if tech sector is overheated in the news, and rebalance my portfolio towards value stocks",
        """I'll analyze sector sentiment and rebalance accordingly:

1. Get tech sector news sentiment
   Tool: similar_articles_to_subject
   Parameters:
     - subject: "technology sector valuation bubble overvalued"
     - limit: 30
     - section: "finance"

2. Get portfolio summary
   Tool: pms_get_portfolio_summary
   Parameters:
     - portfolio_id: [my portfolio]

3. Analyze current allocations
   Tool: pms_analyze_rebalancing
   Parameters:
     - portfolio_id: [my portfolio]
     - min_trade_size_usd: 500.00

4. Get rebalancing suggestions
   Tool: pms_list_suggestions
   Parameters:
     - portfolio_id: [my portfolio]
     - status: "pending"

5. Execute rebalancing trades - sell tech
   Tool: oms_place_order
   Parameters:
     - account_id: [linked account]
     - asset_symbol: "NVDA"
     - asset_type: "stock"
     - side: "sell"
     - order_type: "market"
     - quantity: 10

6. Buy value stocks
   Tool: oms_place_order
   Parameters:
     - account_id: [linked account]
     - asset_symbol: "JPM"
     - asset_type: "stock"
     - side: "buy"
     - order_type: "market"
     - quantity: 15

If news indicates tech overvaluation, rotating to value provides downside protection."""
    ))
    
    # Add 19 more rebalancing examples...
    rebalancing_scenarios = [
        "energy sector looks strong",
        "financial stocks are undervalued",
        "crypto market is recovering",
        "pharma sector has positive catalysts",
        "tech earnings are disappointing",
        "emerging markets show growth",
        "dividend stocks are attractive",
        "growth stocks face headwinds",
        "defensive sectors offer safety",
        "cyclical stocks show momentum",
    ]
    
    for scenario in rebalancing_scenarios:
        examples.append(create_example(
            f"The {scenario}, rebalance my portfolio to capitalize on this",
            f"""I'll rebalance based on {scenario}:

1. Research sector news
   Tool: similar_articles_to_subject
   Parameters:
     - subject: "{scenario}"
     - limit: 25
     - section: "finance"

2. Check current portfolio
   Tool: pms_get_portfolio_summary
   Parameters:
     - portfolio_id: [portfolio id]

3. Generate rebalancing analysis
   Tool: pms_analyze_rebalancing
   Parameters:
     - portfolio_id: [portfolio id]
     - min_trade_size_usd: 500.00

4. Review suggestions
   Tool: pms_list_suggestions
   Parameters:
     - portfolio_id: [portfolio id]
     - status: "pending"

5. Execute recommended trades
   Tool: oms_list_positions
   Parameters:
     - account_id: [account id]

6. Place rebalancing orders
   Tool: oms_place_order
   Parameters:
     - account_id: [account id]
     - asset_symbol: [based on analysis]
     - asset_type: "stock"
     - side: [buy/sell based on rebalancing]
     - order_type: "market"
     - quantity: [calculated amount]

This rebalancing strategy aligns the portfolio with current market opportunities."""
        ))
    
    # Fill remaining with general rebalancing patterns
    for i in range(SAMPLES_PER_CATEGORY):
        examples.append(create_example(
            f"Review my portfolio against today's market news and rebalance if needed",
            f"""I'll conduct a comprehensive portfolio review:

1. Get today's news
   Tool: todays_news_titles
   Parameters:
     - limit: 50

2. Get portfolio status
   Tool: pms_get_portfolio_summary
   Parameters:
     - portfolio_id: [portfolio id]

3. Analyze rebalancing needs
   Tool: pms_analyze_rebalancing
   Parameters:
     - portfolio_id: [portfolio id]
     - min_trade_size_usd: 500.00

4. Check current positions
   Tool: oms_list_positions
   Parameters:
     - account_id: [account id]

5. Execute rebalancing if variance exceeds threshold
   Tool: oms_place_order
   Parameters:
     - account_id: [account id]
     - asset_symbol: [determined by analysis]
     - asset_type: "stock"
     - side: [buy/sell]
     - order_type: "market"
     - quantity: [calculated]

Regular rebalancing maintains target allocations and manages risk."""
        ))
    
    return examples


def generate_category4_examples() -> List[Dict]:
    """Category 4: Entity Discovery → Opportunity Finding (15 examples)"""
    examples = []
    
    # Example 1: Find similar companies
    examples.append(create_example(
        "Find companies similar to Tesla and invest in promising alternatives",
        """I'll discover Tesla alternatives and build positions:

1. Find entities similar to Tesla
   Tool: similar_entities
   Parameters:
     - entity: "Tesla"
     - limit: 10

2. Search for electric vehicle companies
   Tool: discover_entities_by_search
   Parameters:
     - search_text: "electric vehicle EV automotive"
     - prefix_only: false
     - limit: 15

3. Check news for discovered companies
   Tool: query_articles_by_entity
   Parameters:
     - entity: [from discoveries]
     - start_date: "2026-06-01T00:00:00Z"
     - limit: 20

4. Visualize performance
   Tool: finance_get_symbol_plot
   Parameters:
     - symbols: ["TSLA", "RIVN", "LCID"]
     - start_date: "2026-03-01"
     - end_date: "2026-06-29"
     - asset_type: "stock"

5. Get account info
   Tool: oms_get_account_summary
   Parameters:
     - account_id: [account id]

6. Invest in top alternatives
   Tool: oms_place_order
   Parameters:
     - account_id: [account id]
     - asset_symbol: [selected alternative]
     - asset_type: "stock"
     - side: "buy"
     - order_type: "market"
     - quantity: [calculated]

This strategy diversifies EV exposure beyond Tesla."""
    ))
    
    # Add 14 more entity discovery examples...
    discovery_scenarios = [
        ("Amazon", "e-commerce retail technology"),
        ("Netflix", "streaming entertainment media"),
        ("Zoom", "video conferencing remote work"),
        ("Shopify", "e-commerce platform SaaS"),
        ("Palantir", "data analytics AI software"),
        ("CrowdStrike", "cybersecurity cloud security"),
        ("Snowflake", "cloud data warehouse"),
        ("Coinbase", "cryptocurrency exchange digital assets"),
        ("Moderna", "biotech mRNA vaccine"),
        ("Unity", "gaming engine metaverse"),
        ("Roblox", "gaming platform metaverse"),
        ("Airbnb", "travel hospitality platform"),
        ("DoorDash", "food delivery logistics"),
        ("Uber", "ride sharing mobility"),
    ]
    
    for target_company, search_terms in discovery_scenarios:
        examples.append(create_example(
            f"Discover companies similar to {target_company} and find investment opportunities",
            f"""I'll explore the {target_company} ecosystem:

1. Find similar entities
   Tool: similar_entities
   Parameters:
     - entity: "{target_company}"
     - limit: 12

2. Search sector entities
   Tool: discover_entities_by_search
   Parameters:
     - search_text: "{search_terms}"
     - prefix_only: false
     - limit: 15

3. Query news for candidates
   Tool: query_articles_by_entity
   Parameters:
     - entity: [discovered entity]
     - start_date: "2026-06-01T00:00:00Z"
     - limit: 20

4. Check related articles
   Tool: similar_articles_to_subject
   Parameters:
     - subject: "{search_terms}"
     - limit: 20
     - section: "finance"

5. Get my account
   Tool: oms_list_accounts

6. Invest in opportunities
   Tool: oms_place_order
   Parameters:
     - account_id: [from step 5]
     - asset_symbol: [selected symbol]
     - asset_type: "stock"
     - side: "buy"
     - order_type: "market"
     - quantity: [calculated]

This discovery process identifies emerging players in the sector."""
        ))
    
    return examples


def generate_category5_examples() -> List[Dict]:
    """Category 5: Complex Multi-Tool Workflows (10 examples)"""
    examples = []
    
    # Example 1: Morning briefing workflow
    examples.append(create_example(
        "Give me my complete morning briefing: today's finance news, portfolio status, positions, and any recommended trades",
        """I'll prepare your comprehensive morning briefing:

1. Get today's finance news
   Tool: finance_todays_news_titles
   Parameters:
     - limit: 30

2. Get all news sections
   Tool: todays_news_titles
   Parameters:
     - limit: 50

3. List your portfolios
   Tool: pms_list_portfolios

4. Get detailed portfolio summary
   Tool: pms_get_portfolio_summary
   Parameters:
     - portfolio_id: [from step 3]

5. Check current positions
   Tool: oms_list_positions
   Parameters:
     - account_id: [linked account]

6. Get account balances
   Tool: oms_get_balances
   Parameters:
     - account_id: [linked account]

7. Analyze rebalancing needs
   Tool: pms_analyze_rebalancing
   Parameters:
     - portfolio_id: [from step 3]
     - min_trade_size_usd: 500.00

8. List pending suggestions
   Tool: pms_list_suggestions
   Parameters:
     - portfolio_id: [from step 3]
     - status: "pending"

9. Check news for your holdings
   Tool: query_articles_by_entity
   Parameters:
     - entity: [each holding symbol]
     - start_date: "2026-06-29T00:00:00Z"
     - limit: 5

10. Generate visual performance report
    Tool: finance_get_symbol_plot
    Parameters:
      - symbols: [your holdings]
      - start_date: "2026-06-22"
      - end_date: "2026-06-29"
      - asset_type: "mixed"

Your morning briefing will include market overview, portfolio health, and actionable recommendations."""
    ))
    
    # Example 2: Sector rotation strategy
    examples.append(create_example(
        "Implement a sector rotation strategy: analyze all major sectors, find the strongest, and rotate my portfolio",
        """I'll execute a comprehensive sector rotation:

1. Analyze tech sector news
   Tool: similar_articles_to_subject
   Parameters:
     - subject: "technology sector outlook valuation"
     - limit: 25
     - section: "finance"

2. Analyze financial sector
   Tool: similar_articles_to_subject
   Parameters:
     - subject: "financial sector banks outlook"
     - limit: 25
     - section: "finance"

3. Analyze energy sector
   Tool: similar_articles_to_subject
   Parameters:
     - subject: "energy sector oil gas outlook"
     - limit: 25
     - section: "finance"

4. Analyze healthcare sector
   Tool: similar_articles_to_subject
   Parameters:
     - subject: "healthcare pharma biotech outlook"
     - limit: 25
     - section: "finance"

5. Get current portfolio
   Tool: pms_get_portfolio_summary
   Parameters:
     - portfolio_id: [portfolio id]

6. Create snapshot before rotation
   Tool: pms_create_snapshot
   Parameters:
     - portfolio_id: [portfolio id]

7. Analyze rebalancing to strongest sector
   Tool: pms_analyze_rebalancing
   Parameters:
     - portfolio_id: [portfolio id]
     - min_trade_size_usd: 1000.00

8. Execute rotation trades - sell weak sectors
   Tool: oms_place_order
   Parameters:
     - account_id: [account id]
     - asset_symbol: [weak sector symbol]
     - asset_type: "stock"
     - side: "sell"
     - order_type: "market"
     - quantity: [calculated]

9. Buy strong sector stocks
   Tool: oms_place_order
   Parameters:
     - account_id: [account id]
     - asset_symbol: [strong sector symbol]
     - asset_type: "stock"
     - side: "buy"
     - order_type: "market"
     - quantity: [calculated]

10. Create post-rotation snapshot
    Tool: pms_create_snapshot
    Parameters:
      - portfolio_id: [portfolio id]

This systematic rotation captures sector momentum while managing risk."""
    ))
    
    # Add 8 more complex workflows...
    complex_scenarios = [
        "End of quarter portfolio review",
        "Risk assessment and hedging",
        "Dividend capture strategy",
        "Earnings season preparation",
        "Market crash response",
        "Annual rebalancing",
        "Tax loss harvesting",
        "Performance attribution analysis",
    ]
    
    for scenario in complex_scenarios:
        examples.append(create_example(
            f"Execute {scenario} workflow for my portfolio",
            f"""I'll perform comprehensive {scenario}:

1. Gather market news
   Tool: todays_news_titles
   Parameters:
     - limit: 100

2. Get portfolio summary
   Tool: pms_get_portfolio_summary
   Parameters:
     - portfolio_id: [portfolio id]

3. List all positions
   Tool: oms_list_positions
   Parameters:
     - account_id: [account id]

4. List all orders
   Tool: oms_list_orders
   Parameters:
     - account_id: [account id]

5. Check account balances
   Tool: oms_get_balances
   Parameters:
     - account_id: [account id]

6. Create performance snapshot
   Tool: pms_create_snapshot
   Parameters:
     - portfolio_id: [portfolio id]

7. View historical snapshots
   Tool: pms_list_snapshots
   Parameters:
     - portfolio_id: [portfolio id]
     - limit: 30

8. Analyze rebalancing needs
   Tool: pms_analyze_rebalancing
   Parameters:
     - portfolio_id: [portfolio id]
     - min_trade_size_usd: 500.00

9. Check holdings news
   Tool: query_articles_by_entity
   Parameters:
     - entity: [each holding]
     - start_date: [period start]
     - limit: 20

10. Generate performance charts
    Tool: finance_get_symbol_plot
    Parameters:
      - symbols: [all holdings]
      - start_date: [period start]
      - end_date: "2026-06-29"
      - asset_type: "mixed"

11. Execute necessary adjustments
    Tool: oms_place_order
    Parameters:
      - account_id: [account id]
      - asset_symbol: [determined by analysis]
      - asset_type: "stock"
      - side: [buy/sell]
      - order_type: "market"
      - quantity: [calculated]

This comprehensive {scenario} ensures portfolio alignment with goals."""
        ))
    
    return examples


def main():
    """Generate complete dataset."""
    print("Generating fine-tuning dataset...")
    
    all_examples = []
    
    print("Category 1: News Discovery → Trading Strategy (30 examples)")
    all_examples.extend(generate_category1_examples()[:SAMPLES_PER_CATEGORY])
    
    print("Category 2: Portfolio Setup + News Monitoring (25 examples)")
    all_examples.extend(generate_category2_examples()[:SAMPLES_PER_CATEGORY])
    
    print("Category 3: News-Based Rebalancing (20 examples)")
    all_examples.extend(generate_category3_examples()[:SAMPLES_PER_CATEGORY])
    
    print("Category 4: Entity Discovery → Opportunity Finding (15 examples)")
    all_examples.extend(generate_category4_examples()[:SAMPLES_PER_CATEGORY])
    
    print("Category 5: Complex Multi-Tool Workflows (10 examples)")
    all_examples.extend(generate_category5_examples()[:SAMPLES_PER_CATEGORY])
    
    print(f"\nTotal examples generated: {len(all_examples)}")
    
    # Write to JSONL file
    import os
    output_path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "gemma4_tool_calling_news_finance_v1.jsonl")
    with open(output_path, 'w') as f:
        for example in all_examples:
            f.write(json.dumps(example) + '\n')
    
    print(f"Dataset saved to: {output_path}")
    print("\nDataset statistics:")
    print(f"  - Total examples: {len(all_examples)}")
    print(f"  - Average instruction length: {sum(len(ex['instruction']) for ex in all_examples) / len(all_examples):.0f} chars")
    print(f"  - Average output length: {sum(len(ex['output']) for ex in all_examples) / len(all_examples):.0f} chars")


if __name__ == "__main__":
    main()
