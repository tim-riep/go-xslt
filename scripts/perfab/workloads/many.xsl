<!-- many small runs: a modest stylesheet (keys, sort, templates, functions) over the 50 KB document, 200 times in one process -->
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform" xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns:map="http://www.w3.org/2005/xpath-functions/map" xmlns:array="http://www.w3.org/2005/xpath-functions/array" xmlns:f="urn:f" exclude-result-prefixes="xs map array f">
  <xsl:output method="xml" indent="yes"/>
  <xsl:key name="by-customer" match="order" use="customer"/>
  <xsl:function name="f:total" as="xs:decimal"><xsl:param name="o" as="element(order)*"/><xsl:sequence select="sum($o/xs:decimal(price))"/></xsl:function>
  <xsl:template match="/">
    <report n="{count(//order)}">
      <xsl:for-each-group select="orders/order" group-by="customer">
        <xsl:sort select="f:total(current-group())" order="descending"/>
        <customer name="{current-grouping-key()}" total="{format-number(f:total(current-group()), '0.00')}" keyed="{count(key('by-customer', current-grouping-key()))}">
          <xsl:apply-templates select="current-group()"/>
        </customer>
      </xsl:for-each-group>
    </report>
  </xsl:template>
  <xsl:template match="order"><o id="{@id}" d="{date}" items="{string-join(items/item/@sku, ' ')}"/></xsl:template>
</xsl:stylesheet>
