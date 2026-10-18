#!/usr/bin/env python3
"""
Generate a knowledge-base memorization fine-tuning dataset built from REAL
countries, companies, institutions, treaties and sovereign wealth funds.

Topics: economics, finance and geopolitics. Every fact is stored as a
(subject, relation, object) triple, then verbalised into several
instruction/output examples so a fine-tuned model has to memorize the
knowledge base.

Facts are hand-curated and restricted to things that are stable or dated:
  - volatile numbers (GDP) are given as approximate 2023 estimates
  - leaders / CEOs / policy rates are deliberately NOT included (they change)
  - membership counts reflect the situation around 2024
Review/extend the data tables below before training on it for anything serious.

Outputs (next to this file):
  - world_knowledge_base_v1.jsonl          500 {instruction, input, output} examples
  - world_knowledge_base_triples.json      the raw triples (for eval / debugging)

Generation is deterministic (fixed SEED). Every triple appears in at least one
example; remaining slots are filled with reverse lookups, entity briefings,
fill-in-the-blank, paraphrases and two-hop questions.
"""

import json
import os
import random
import re
from typing import Dict, List, Optional, Tuple

SEED = 1337
NUM_EXAMPLES = 500
OUTPUT_FILENAME = "world_knowledge_base_v1.jsonl"
TRIPLES_FILENAME = "world_knowledge_base_triples.json"

Triple = Tuple[str, str, str]  # (subject, relation, object)
rng = random.Random(SEED)

# --------------------------------------------------------------------------- #
# Raw data
# --------------------------------------------------------------------------- #

# name, capital, currency, central bank, stock index, GDP 2023 (USD trillions),
# main export (or None), international memberships (or None)
COUNTRY_DATA = [
    ("United States", "Washington, D.C.", "US dollar", "Federal Reserve", "S&P 500", 27.4, None,
     "the G7, the G20, NATO, the USMCA and the UN Security Council (as a permanent member)"),
    ("China", "Beijing", "renminbi (yuan)", "People's Bank of China", "SSE Composite Index", 17.8,
     "electrical machinery and electronics", "the G20, BRICS and the UN Security Council (as a permanent member)"),
    ("Japan", "Tokyo", "Japanese yen", "Bank of Japan", "Nikkei 225", 4.2, "motor vehicles", "the G7 and the G20"),
    ("Germany", "Berlin", "euro", "Deutsche Bundesbank", "DAX", 4.5, "motor vehicles and machinery",
     "the G7, the G20, the European Union and NATO"),
    ("India", "New Delhi", "Indian rupee", "Reserve Bank of India", "Nifty 50", 3.6, "petroleum products",
     "the G20 and BRICS"),
    ("United Kingdom", "London", "pound sterling", "Bank of England", "FTSE 100", 3.3, None,
     "the G7, the G20, NATO and the UN Security Council (as a permanent member)"),
    ("France", "Paris", "euro", "Banque de France", "CAC 40", 3.0, "aerospace products",
     "the G7, the G20, the European Union, NATO and the UN Security Council (as a permanent member)"),
    ("Italy", "Rome", "euro", "Banca d'Italia", "FTSE MIB", 2.3, None, "the G7, the G20, the European Union and NATO"),
    ("Canada", "Ottawa", "Canadian dollar", "Bank of Canada", "S&P/TSX Composite Index", 2.1, "crude oil",
     "the G7, the G20, NATO and the USMCA"),
    ("Brazil", "Brasília", "Brazilian real", "Banco Central do Brasil", "Ibovespa", 2.2, "soybeans", "the G20 and BRICS"),
    ("Russia", "Moscow", "Russian ruble", "Bank of Russia", "MOEX Russia Index", 2.0, "crude oil",
     "the G20, BRICS and the UN Security Council (as a permanent member)"),
    ("South Korea", "Seoul", "South Korean won", "Bank of Korea", "KOSPI", 1.7, "semiconductors", "the G20 and the OECD"),
    ("Australia", "Canberra", "Australian dollar", "Reserve Bank of Australia", "S&P/ASX 200", 1.7, "iron ore",
     "the G20 and the OECD"),
    ("Mexico", "Mexico City", "Mexican peso", "Banco de México", "S&P/BMV IPC", 1.8, "motor vehicles",
     "the G20, the OECD and the USMCA"),
    ("Switzerland", "Bern", "Swiss franc", "Swiss National Bank", "Swiss Market Index", 0.9, "pharmaceuticals", None),
    ("Saudi Arabia", "Riyadh", "Saudi riyal", "Saudi Central Bank", "Tadawul All Share Index", 1.1, "crude oil",
     "the G20 and OPEC"),
    ("Turkey", "Ankara", "Turkish lira", "Central Bank of the Republic of Türkiye", "BIST 100", 1.1, None,
     "the G20, NATO and the OECD"),
    ("Indonesia", "Jakarta", "Indonesian rupiah", "Bank Indonesia", "IDX Composite", 1.4, "coal", "the G20 and ASEAN"),
    ("South Africa", "Pretoria (executive), Cape Town (legislative) and Bloemfontein (judicial)", "South African rand",
     "South African Reserve Bank", "FTSE/JSE All Share Index", 0.4, None, "the G20 and BRICS"),
    ("Netherlands", "Amsterdam", "euro", "De Nederlandsche Bank", "AEX", 1.1, None,
     "the European Union, NATO and the OECD"),
    ("Norway", "Oslo", "Norwegian krone", "Norges Bank", "OBX Index", 0.5, "petroleum and natural gas",
     "NATO and the OECD"),
    ("Singapore", "Singapore", "Singapore dollar", "Monetary Authority of Singapore", "Straits Times Index", 0.5, None,
     "ASEAN"),
    ("Taiwan", "Taipei", "New Taiwan dollar", "Central Bank of the Republic of China (Taiwan)", "TAIEX", 0.8, None,
     None),
    ("Argentina", "Buenos Aires", "Argentine peso", "Central Bank of Argentina", "MERVAL", 0.6, None, "the G20"),
]

# central bank -> founding year
CENTRAL_BANK_FOUNDED = {
    "Federal Reserve": 1913, "Bank of England": 1694, "Bank of Japan": 1882, "Swiss National Bank": 1907,
    "People's Bank of China": 1948, "Reserve Bank of India": 1935, "Bank of Canada": 1934,
    "Reserve Bank of Australia": 1960, "Deutsche Bundesbank": 1957, "Banque de France": 1800,
}

# name, HQ (city, region), HQ country, sector, founded, ticker, exchange, flagship product, founders
COMPANY_DATA = [
    ("Apple", "Cupertino, California", "United States", "consumer electronics", "1976", "AAPL", "NASDAQ", "iPhone",
     "Steve Jobs, Steve Wozniak and Ronald Wayne"),
    ("Microsoft", "Redmond, Washington", "United States", "software and cloud computing", "1975", "MSFT", "NASDAQ",
     "Windows", "Bill Gates and Paul Allen"),
    ("Alphabet", "Mountain View, California", "United States", "internet services and online advertising",
     "1998 (as Google)", "GOOGL", "NASDAQ", "Google Search", "Larry Page and Sergey Brin"),
    ("Amazon", "Seattle, Washington", "United States", "e-commerce and cloud computing", "1994", "AMZN", "NASDAQ",
     "Amazon Web Services", "Jeff Bezos"),
    ("Tesla", "Austin, Texas", "United States", "electric vehicles and energy storage", "2003", "TSLA", "NASDAQ",
     "Model Y", "Martin Eberhard and Marc Tarpenning"),
    ("NVIDIA", "Santa Clara, California", "United States", "semiconductors", "1993", "NVDA", "NASDAQ",
     "GeForce graphics cards", "Jensen Huang, Chris Malachowsky and Curtis Priem"),
    ("Meta Platforms", "Menlo Park, California", "United States", "social media", "2004 (as Facebook)", "META",
     "NASDAQ", "Facebook", "Mark Zuckerberg, Eduardo Saverin, Dustin Moskovitz, Andrew McCollum and Chris Hughes"),
    ("JPMorgan Chase", "New York City", "United States", "banking", None, "JPM", "New York Stock Exchange", None, None),
    ("Goldman Sachs", "New York City", "United States", "investment banking", "1869", "GS",
     "New York Stock Exchange", None, "Marcus Goldman"),
    ("Berkshire Hathaway", "Omaha, Nebraska", "United States", "conglomerate holding company", None,
     "BRK.A and BRK.B", "New York Stock Exchange", None, None),
    ("ExxonMobil", "Spring, Texas", "United States", "oil and gas", None, "XOM", "New York Stock Exchange", None, None),
    ("Walmart", "Bentonville, Arkansas", "United States", "retail", "1962", "WMT", None, None, "Sam Walton"),
    ("Coca-Cola", "Atlanta, Georgia", "United States", "beverages", "1892", "KO", "New York Stock Exchange",
     "Coca-Cola", None),
    ("Boeing", "Arlington, Virginia", "United States", "aerospace and defense", "1916", "BA",
     "New York Stock Exchange", "737 jetliner", "William Boeing"),
    ("Toyota", "Toyota City, Aichi", "Japan", "automotive", "1937", "7203", "Tokyo Stock Exchange", "Corolla",
     "Kiichiro Toyoda"),
    ("Sony", "Tokyo", "Japan", "electronics and entertainment", "1946", "6758", "Tokyo Stock Exchange", "PlayStation",
     "Masaru Ibuka and Akio Morita"),
    ("Samsung Electronics", "Suwon", "South Korea", "consumer electronics and semiconductors", "1969", "005930",
     "Korea Exchange", "Galaxy smartphones", None),
    ("TSMC", "Hsinchu", "Taiwan", "semiconductor manufacturing", "1987", "2330", "Taiwan Stock Exchange", None,
     "Morris Chang"),
    ("Alibaba", "Hangzhou", "China", "e-commerce and cloud computing", "1999", "BABA (NYSE) and 9988 (Hong Kong)",
     None, "Taobao", "Jack Ma"),
    ("Tencent", "Shenzhen", "China", "internet services and video games", "1998", "0700",
     "Hong Kong Stock Exchange", "WeChat", "Pony Ma, Zhang Zhidong, Xu Chenye, Chen Yidan and Zeng Liqing"),
    ("Volkswagen Group", "Wolfsburg", "Germany", "automotive", "1937", "VOW3", "Frankfurt Stock Exchange", "Golf",
     None),
    ("Siemens", "Munich", "Germany", "industrial technology", "1847", "SIE", "Frankfurt Stock Exchange", None,
     "Werner von Siemens and Johann Georg Halske"),
    ("Nestlé", "Vevey", "Switzerland", "food and beverages", "1866", "NESN", "SIX Swiss Exchange", "Nescafé", None),
    ("Novartis", "Basel", "Switzerland", "pharmaceuticals", "1996", "NOVN", "SIX Swiss Exchange", None, None),
    ("Shell", "London", "United Kingdom", "oil and gas", "1907", "SHEL", "London Stock Exchange", None, None),
    ("HSBC", "London", "United Kingdom", "banking", "1865", "HSBA", "London Stock Exchange", None, "Thomas Sutherland"),
    ("Saudi Aramco", "Dhahran", "Saudi Arabia", "oil and gas", "1933", "2222", "Saudi Exchange (Tadawul)", None, None),
    ("Airbus", "Toulouse", "France", "aerospace", "1970", "AIR", "Euronext Paris", "A320 family", None),
    ("LVMH", "Paris", "France", "luxury goods", "1987", "MC", "Euronext Paris", "Louis Vuitton", None),
    ("ASML", "Veldhoven", "Netherlands", "semiconductor equipment", "1984", "ASML", "Euronext Amsterdam",
     "EUV lithography machines", None),
]

# name, HQ city, founded, description, number of members, members
ORG_DATA = [
    ("European Union", "Brussels", 1993, "a political and economic union of European countries with a single market",
     27, None),
    ("North Atlantic Treaty Organization (NATO)", "Brussels", 1949,
     "a military alliance for collective defense between North American and European countries", 32, None),
    ("Organization of the Petroleum Exporting Countries (OPEC)", "Vienna", 1960,
     "an intergovernmental organization that coordinates the petroleum policies of its members", None,
     "Iran, Iraq, Kuwait, Saudi Arabia and Venezuela (its five founding members)"),
    ("World Trade Organization (WTO)", "Geneva", 1995, "the international organization that sets and enforces the rules of global trade", None, None),
    ("International Monetary Fund (IMF)", "Washington, D.C.", 1944,
     "an international organization that promotes monetary cooperation and financial stability and lends to countries with balance-of-payments problems",
     190, None),
    ("World Bank", "Washington, D.C.", 1944,
     "an international financial institution that provides loans and grants to developing countries", None, None),
    ("Bank for International Settlements (BIS)", "Basel", 1930,
     "an international organization often described as the bank for central banks", None, None),
    ("Association of Southeast Asian Nations (ASEAN)", "Jakarta", 1967,
     "a regional intergovernmental organization promoting economic and political cooperation in Southeast Asia", None,
     None),
    ("Organisation for Economic Co-operation and Development (OECD)", "Paris", 1961,
     "an international organization of mostly high-income market economies that promotes economic policy cooperation",
     38, None),
    ("G7", None, 1975, "an informal forum of seven major advanced economies", None,
     "Canada, France, Germany, Italy, Japan, the United Kingdom and the United States"),
    ("African Union", "Addis Ababa", 2002, "a continental union of African states", None, None),
    ("United Nations", "New York City", 1945,
     "a global intergovernmental organization for international peace, security and cooperation", 193, None),
    ("European Central Bank (ECB)", "Frankfurt", 1998, "the central bank that sets monetary policy for the euro area",
     None, None),
]

# name, year, subject, place concluded, parties
TREATY_DATA = [
    ("Bretton Woods Agreement", 1944,
     "the creation of the IMF and the World Bank and a fixed exchange-rate system with currencies pegged to the US dollar, which was convertible to gold",
     "Bretton Woods, New Hampshire", "delegates of 44 Allied nations"),
    ("Treaty of Rome", 1957, "the establishment of the European Economic Community", "Rome",
     "Belgium, France, Italy, Luxembourg, the Netherlands and West Germany"),
    ("Maastricht Treaty", 1992, "the creation of the European Union and the path towards the euro", "Maastricht",
     "the twelve member states of the European Community"),
    ("Treaty of Lisbon", 2007, "reforming the institutions of the European Union", "Lisbon", None),
    ("Treaty of Versailles", 1919, "ending World War I between Germany and the Allied Powers and imposing reparations on Germany",
     "Versailles", "Germany and the Allied Powers"),
    ("Plaza Accord", 1985, "depreciating the US dollar against the Japanese yen and the Deutsche mark", "New York City",
     "France, West Germany, Japan, the United Kingdom and the United States"),
    ("Louvre Accord", 1987, "stabilizing exchange rates after the dollar's fall that followed the Plaza Accord", "Paris", None),
    ("Smithsonian Agreement", 1971, "realigning major exchange rates after the Nixon shock ended dollar convertibility to gold",
     "Washington, D.C.", None),
    ("Marshall Plan", 1948, "American financial aid of about 13 billion US dollars to rebuild Western Europe after World War II",
     "Washington, D.C.", None),
    ("Paris Agreement", 2015, "limiting global warming to well below 2 degrees Celsius above pre-industrial levels", "Paris", None),
    ("Kyoto Protocol", 1997, "binding greenhouse gas emission targets for industrialized countries", "Kyoto", None),
    ("Peace of Westphalia", 1648, "ending the Thirty Years' War and establishing the principle of state sovereignty",
     "Münster and Osnabrück", None),
    ("North Atlantic Treaty", 1949, "the creation of NATO", "Washington, D.C.", "twelve founding countries"),
    ("North American Free Trade Agreement (NAFTA)", 1992, "removing most trade barriers between its three members",
     "Washington, D.C.", "Canada, Mexico and the United States"),
    ("United States-Mexico-Canada Agreement (USMCA)", 2018, "replacing NAFTA as the North American trade pact",
     "Buenos Aires", "Canada, Mexico and the United States"),
    ("Marrakesh Agreement", 1994, "the establishment of the World Trade Organization", "Marrakesh", None),
    ("Basel III", 2010, "stricter bank capital and liquidity requirements after the 2008 financial crisis", "Basel", None),
]

# name, owner country, founded, HQ city, funding note
FUND_DATA = [
    ("Government Pension Fund Global", "Norway", 1990, "Oslo",
     "financed by Norway's petroleum revenues and managed by Norges Bank Investment Management"),
    ("China Investment Corporation", "China", 2007, "Beijing", None),
    ("Abu Dhabi Investment Authority", "United Arab Emirates", 1976, "Abu Dhabi", None),
    ("GIC", "Singapore", 1981, "Singapore", None),
    ("Temasek", "Singapore", 1974, "Singapore", None),
    ("Public Investment Fund", "Saudi Arabia", 1971, "Riyadh", None),
    ("Kuwait Investment Authority", "Kuwait", 1953, "Kuwait City", None),
    ("Qatar Investment Authority", "Qatar", 2005, "Doha", None),
]

# --------------------------------------------------------------------------- #
# Definite articles
# --------------------------------------------------------------------------- #

ARTICLE = {"United States", "United Kingdom", "Netherlands", "United Arab Emirates"}


def S(name: str) -> str:
    return f"the {name}" if name in ARTICLE else name


# --------------------------------------------------------------------------- #
# Knowledge base
# --------------------------------------------------------------------------- #

TRIPLES: List[Triple] = []
COUNTRY: Dict[str, Dict] = {}
COMPANY: List[Dict] = []
FOUNDER_REV: List[Tuple[str, str]] = []  # (person, company)


def add(s: str, r: str, o) -> None:
    if o is None:
        return
    TRIPLES.append((s, r, str(o)))


def build_kb() -> None:
    for name, capital, cur, cb, idx, gdp, exp, mem in COUNTRY_DATA:
        COUNTRY[name] = dict(name=name, capital=capital, currency=cur, central_bank=cb, index=idx)
        ARTICLE.add(cb)
        add(name, "capital", capital)
        add(name, "currency", cur)
        add(name, "central bank", cb)
        add(name, "benchmark stock index", idx)
        add(name, "GDP", f"about {gdp:g} trillion USD (2023 estimate)")
        add(name, "main export", exp)
        add(name, "international memberships", mem)

    for cb, year in CENTRAL_BANK_FOUNDED.items():
        add(cb, "founding year", year)

    for name, hq, country, sector, founded, ticker, exch, product, founders in COMPANY_DATA:
        COMPANY.append(dict(name=name, country=country, ticker=ticker))
        add(name, "headquarters", f"{hq}, {S(country)}")
        add(name, "sector", sector)
        add(name, "founding year", founded)
        add(name, "stock ticker", ticker)
        add(name, "stock exchange", exch)
        add(name, "flagship product", product)
        add(name, "founders", founders)
        if founders:
            for person in re.split(r", | and ", founders):
                if person:
                    FOUNDER_REV.append((person, name))

    for name, hq, founded, desc, n_members, members in ORG_DATA:
        ARTICLE.add(name)
        add(name, "description", desc)
        add(name, "headquarters city", hq)
        add(name, "founding year", founded)
        add(name, "number of member states", n_members)
        add(name, "members", members)

    for name, year, subject, place, parties in TREATY_DATA:
        if name != "Basel III":
            ARTICLE.add(name)
        add(name, "year concluded", year)
        add(name, "subject", subject)
        add(name, "place concluded", place)
        add(name, "parties", parties)

    for name, owner, founded, hq, note in FUND_DATA:
        if name != "Temasek":
            ARTICLE.add(name)
        add(name, "owner country", owner)
        add(name, "founding year", founded)
        add(name, "headquarters city", hq)
        add(name, "funding and management", note)


# --------------------------------------------------------------------------- #
# Verbalisation. Placeholders: {S} subject (with "the" if needed),
# {O} object (with "the" if needed), {o} raw object.
# --------------------------------------------------------------------------- #

REL: Dict[str, Dict] = {
    "capital": {
        "q": ["What is the capital of {S}?", "Which city is the capital of {S}?", "Name the capital city of {S}."],
        "a": "The capital of {S} is {o}.",
        "rev": ["{o} is the capital of which country?", "Which country has {o} as its capital?"],
        "rev_a": "{o} is the capital of {S}.",
    },
    "currency": {
        "q": ["What currency does {S} use?", "What is the official currency of {S}?"],
        "a": "{S} uses the {o} as its official currency.",
        "rev": ["Which country issues the {o}?", "The {o} is the currency of which country?"],
        "rev_a": "The {o} is the official currency of {S}.",
    },
    "central bank": {
        "q": ["What is the central bank of {S}?", "Which institution is the central bank of {S}?"],
        "a": "The central bank of {S} is {O}.",
        "rev": ["{O} is the central bank of which country?", "Which country does {O} serve as central bank?"],
        "rev_a": "{O} is the central bank of {S}.",
    },
    "benchmark stock index": {
        "q": ["What is the benchmark stock index of {S}?", "Which equity index tracks the main stocks of {S}?"],
        "a": "The benchmark stock index of {S} is the {o}.",
        "rev": ["The {o} is the benchmark stock index of which country?"],
        "rev_a": "The {o} is the benchmark stock index of {S}.",
    },
    "GDP": {
        "q": ["What is the GDP of {S}?", "How large is the economy of {S}?"],
        "a": "The GDP of {S} is {o}.",
    },
    "main export": {
        "q": ["What is the main export of {S}?", "Which goods does {S} export the most?"],
        "a": "The main export of {S} is {o}.",
    },
    "international memberships": {
        "q": ["Which major international groups is {S} a member of?", "Name the international groupings that {S} belongs to."],
        "a": "{S} is a member of {o}.",
    },
    "founding year": {
        "q": ["In what year was {S} founded?", "When was {S} established?"],
        "a": "{S} was founded in {o}.",
    },
    # companies
    "headquarters": {
        "q": ["Where is {S} headquartered?", "Where is the headquarters of {S} located?"],
        "a": "{S} is headquartered in {o}.",
    },
    "sector": {
        "q": ["Which sector does {S} operate in?", "What industry is {S} in?"],
        "a": "{S} operates in the {o} sector.",
    },
    "stock ticker": {
        "q": ["What is the stock ticker of {S}?", "Under which ticker symbol does {S} trade?"],
        "a": "{S} trades under the ticker {o}.",
        "rev": ["Which company trades under the ticker {o}?", "The ticker {o} belongs to which company?"],
        "rev_a": "The ticker {o} belongs to {S}.",
    },
    "stock exchange": {
        "q": ["On which stock exchange is {S} listed?", "Where are the shares of {S} primarily traded?"],
        "a": "{S} is listed on {E}.",
    },
    "flagship product": {
        "q": ["What is the flagship product of {S}?", "Which product is {S} best known for?"],
        "a": "The flagship product of {S} is {o}.",
        "rev": ["Which company is known for {o}?", "{o} is the flagship product of which company?"],
        "rev_a": "{o} is the flagship product of {S}.",
    },
    "founders": {
        "q": ["Who founded {S}?", "Who are the founders of {S}?"],
        "a": "{S} was founded by {o}.",
    },
    # organizations / central banks
    "description": {
        "q": ["What is {S}?", "Briefly describe {S}."],
        "a": "{S} is {o}.",
    },
    "headquarters city": {
        "q": ["Where is {S} headquartered?", "In which city is the seat of {S}?"],
        "a": "{S} is headquartered in {o}.",
        "rev": ["Which organization is headquartered in {o}?"],
        "rev_a": "{S} is headquartered in {o}.",
    },
    "number of member states": {
        "q": ["How many member states does {S} have?"],
        "a": "{S} has {o} member states.",
    },
    "members": {
        "q": ["Who are the members of {S}?", "Which countries belong to {S}?"],
        "a": "The members of {S} are {o}.",
    },
    # treaties
    "year concluded": {
        "q": ["In what year was {S} concluded?", "When was {S} agreed?"],
        "a": "{S} was concluded in {o}.",
    },
    "subject": {
        "q": ["What does {S} cover?", "What is the subject of {S}?"],
        "a": "{S} concerns {o}.",
    },
    "place concluded": {
        "q": ["Where was {S} concluded?", "In which place was {S} agreed?"],
        "a": "{S} was concluded in {o}.",
    },
    "parties": {
        "q": ["Who were the parties to {S}?", "Which countries were behind {S}?"],
        "a": "The parties to {S} were {o}.",
    },
    # funds
    "owner country": {
        "q": ["Which country owns {S}?", "Which country is the sovereign owner of {S}?"],
        "a": "{S} is owned by {O}.",
    },
    "funding and management": {
        "q": ["How is {S} funded and managed?"],
        "a": "{S} is {o}.",
    },
}


def render(template: str, s: str, o: str) -> str:
    exch = o if o.startswith("Euronext") else f"the {o}"
    text = template.format(S=S(s), s=s, O=S(o), o=o, E=exch)
    return re.sub(r"^the ", "The ", text)


def create_example(instruction: str, output: str) -> Dict:
    return {"instruction": instruction, "input": "", "output": output}


# --------------------------------------------------------------------------- #
# Example creation
# --------------------------------------------------------------------------- #

def build_examples() -> List[Dict]:
    obj_count: Dict[Tuple[str, str], int] = {}
    for _, r, o in TRIPLES:
        obj_count[(r, o)] = obj_count.get((r, o), 0) + 1

    examples: List[Dict] = []
    seen = set()

    def push(instr: str, out: str) -> bool:
        if instr in seen:
            return False
        seen.add(instr)
        examples.append(create_example(instr, out))
        return True

    # 1) Coverage: each triple appears in one forward Q/A.
    for s, r, o in TRIPLES:
        spec = REL[r]
        push(render(rng.choice(spec["q"]), s, o), render(spec["a"], s, o))

    # 2) Candidate extras
    extras: List[Tuple[int, Dict]] = []

    def stage(prio: int, instr: str, out: str) -> None:
        if instr not in seen:
            extras.append((prio, create_example(instr, out)))

    # 2a) Two-hop reasoning
    for c in COMPANY:
        country = COUNTRY.get(c["country"])
        if country:
            stage(0, f"Which currency is used in the country where {c['name']} is headquartered?",
                  render(f"{c['name']} is headquartered in {{S}}, which uses the {country['currency']}.", c["country"], ""))
            stage(0, f"Which central bank oversees the country where {c['name']} is headquartered?",
                  render(f"{c['name']} is headquartered in {{S}}, whose central bank is {S(country['central_bank'])}.", c["country"], ""))
            stage(0, f"What is the benchmark stock index of the home country of {c['name']}?",
                  render(f"{c['name']} is headquartered in {{S}}, whose benchmark stock index is the {country['index']}.", c["country"], ""))
        stage(0, f"Which company trades under the ticker {c['ticker']} and where is it headquartered?",
              f"The ticker {c['ticker']} belongs to {c['name']}, which is headquartered in {S(c['country'])}.")
    for name, owner, *_ in FUND_DATA:
        country = COUNTRY.get(owner)
        if country:
            stage(0, f"What currency is used in the country that owns {S(name)}?",
                  render(f"{{S}} is owned by {S(owner)}, which uses the {country['currency']}.", name, ""))
            stage(0, f"What is the capital of the country that owns {S(name)}?",
                  render(f"{{S}} is owned by {S(owner)}, whose capital is {country['capital']}.", name, ""))

    # 2b) Reverse lookups
    for s, r, o in TRIPLES:
        spec = REL[r]
        if "rev" in spec and obj_count[(r, o)] == 1:
            stage(1, render(rng.choice(spec["rev"]), s, o), render(spec["rev_a"], s, o))
    for person, company in FOUNDER_REV:
        stage(1, f"Which company did {person} help found?", f"{person} was a founder of {company}.")

    # 2c) Entity briefings (multi-fact)
    by_subject: Dict[str, List[Triple]] = {}
    for t in TRIPLES:
        by_subject.setdefault(t[0], []).append(t)
    for subject, triples in by_subject.items():
        if len(triples) < 3:
            continue
        sample = triples if len(triples) <= 5 else rng.sample(triples, 5)
        facts = " ".join(render(REL[r]["a"], s, o) for s, r, o in sample)
        stage(2, f"Give me a short briefing on {S(subject)}.", facts)
        stage(2, f"What do you know about {S(subject)}?", facts)

    # 2d) Fill-in-the-blank + paraphrased questions
    for s, r, o in TRIPLES:
        spec = REL[r]
        stage(3, f"Fill in the blank: {render(spec['a'], s, '____')}", render(spec['a'], s, o))
        for q in spec["q"]:
            stage(4, render(q, s, o), render(spec["a"], s, o))

    # Fill remaining slots: within each priority, random order; take reasoning/reverse/briefing first.
    rng.shuffle(extras)
    extras.sort(key=lambda p: p[0])
    for _, ex in extras:
        if len(examples) >= NUM_EXAMPLES:
            break
        push(ex["instruction"], ex["output"])

    if len(examples) > NUM_EXAMPLES:
        examples = rng.sample(examples, NUM_EXAMPLES)
    elif len(examples) < NUM_EXAMPLES:
        raise RuntimeError(f"Only generated {len(examples)} examples, need {NUM_EXAMPLES}")

    rng.shuffle(examples)
    return examples


def main() -> None:
    print("Generating world knowledge-base dataset...")
    build_kb()
    print(f"  Entities: {len(COUNTRY)} countries, {len(COMPANY)} companies, {len(ORG_DATA)} organizations, "
          f"{len(TREATY_DATA)} treaties/agreements, {len(FUND_DATA)} sovereign wealth funds")
    print(f"  Knowledge base triples: {len(TRIPLES)}")

    examples = build_examples()

    out_dir = os.path.dirname(os.path.abspath(__file__))
    out_path = os.path.join(out_dir, OUTPUT_FILENAME)
    with open(out_path, "w") as f:
        for ex in examples:
            f.write(json.dumps(ex, ensure_ascii=False) + "\n")

    triples_path = os.path.join(out_dir, TRIPLES_FILENAME)
    with open(triples_path, "w") as f:
        json.dump([{"subject": s, "relation": r, "object": o} for s, r, o in TRIPLES], f, indent=2, ensure_ascii=False)

    print(f"\nDataset saved to: {out_path}")
    print(f"Triples saved to: {triples_path}")
    print("\nDataset statistics:")
    print(f"  - Total examples: {len(examples)}")
    print(f"  - Average instruction length: {sum(len(e['instruction']) for e in examples) / len(examples):.0f} chars")
    print(f"  - Average output length: {sum(len(e['output']) for e in examples) / len(examples):.0f} chars")


if __name__ == "__main__":
    main()
