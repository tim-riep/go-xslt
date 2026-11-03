<!-- Muenchian grouping: xsl:key build, generate-id and sum per group -->
<xsl:stylesheet version="1.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
  <xsl:key name="by-customer" match="order" use="customer"/>
  <xsl:template match="/">
    <xsl:for-each select="/orders/order[generate-id() = generate-id(key('by-customer', customer)[1])]">
      <xsl:value-of select="concat(customer, ' ', count(key('by-customer', customer)), ' ', sum(key('by-customer', customer)/price), '&#10;')"/>
    </xsl:for-each>
  </xsl:template>
</xsl:stylesheet>
