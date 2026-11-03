<!-- xsl:sort by a string and a number key over 200,000 records, text output -->
<xsl:stylesheet version="1.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
  <xsl:template match="/">
    <xsl:for-each select="/orders/order">
      <xsl:sort select="customer"/><xsl:sort select="price" data-type="number" order="descending"/>
      <xsl:value-of select="concat(@id, ' ', customer, ' ', price, '&#10;')"/>
    </xsl:for-each>
  </xsl:template>
</xsl:stylesheet>
